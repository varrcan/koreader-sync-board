package convert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/singleflight"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

var (
	// ErrNotConvertible — формат файла не FB2.
	ErrNotConvertible = errors.New("file format is not convertible")
	// ErrUnavailable — исходный файл не открывается.
	ErrUnavailable = errors.New("source file is unavailable")
	// ErrFailed — конвертер не справился с файлом. Текст ошибки — только для лога.
	ErrFailed = errors.New("conversion failed")
	// ErrTimeout — срок вышел в очереди или во время работы.
	ErrTimeout = errors.New("conversion timed out")
)

const (
	// Timeout — общий срок ожидания очереди и конвертации.
	Timeout = 5 * time.Minute
	// memLimit — мягкий предел памяти дочернего процесса: книга у границы 128 МиБ не
	// должна раздувать кучу сборщиком мусора на маленькой VM.
	memLimit = "1GiB"
	// maxStderr ограничивает сохраняемый вывод дочернего процесса: стек паники длинный.
	maxStderr = 64 << 10
	// tmpResult короткий и фиксированный: длинное имя, которое укладывается в 255 байт
	// как .epub, не должно упереться в предел из-за суффикса.
	tmpResult = ".out.tmp"
	maxName   = 255
)

// Service выполняет конвертации по одной и хранит результаты в каталоге данных.
type Service struct {
	store           *library.Store
	exe             string
	converted, temp string
	log             *slog.Logger
	ctx             context.Context
	timeout         time.Duration
	group           singleflight.Group
	sem             chan struct{}
	wg              sync.WaitGroup
}

// Option настраивает Service.
type Option func(*Service)

// WithExecutable задаёт программу дочернего процесса вместо текущего бинарника.
func WithExecutable(exe string) Option { return func(s *Service) { s.exe = exe } }

// WithTimeout задаёт срок вместо Timeout.
func WithTimeout(d time.Duration) Option { return func(s *Service) { s.timeout = d } }

// New готовит каталоги в dataDir и служебный корень. Отмена ctx завершает идущие
// конвертации: задача живёт отдельно от запроса, который её начал.
func New(ctx context.Context, store *library.Store, dataDir string, log *slog.Logger, opts ...Option) (*Service, error) {
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	s := &Service{store: store, exe: exe, converted: filepath.Join(dir, "converted"), temp: filepath.Join(dir, "tmp"),
		log: log, ctx: ctx, timeout: Timeout, sem: make(chan struct{}, 1)}
	for _, o := range opts {
		o(s)
	}
	// Рабочие каталоги, оставшиеся после падения сервера.
	if err := os.RemoveAll(s.temp); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.temp, 0o750); err != nil {
		return nil, err
	}
	if err := store.EnsureManagedRoot(ctx, s.converted); err != nil {
		return nil, err
	}
	return s, nil
}

// Wait ждёт завершения задач не дольше timeout. Вызывается после отмены контекста New.
func (s *Service) Wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		s.log.Warn("library: conversion did not stop", "timeout", timeout)
	}
}

// Convert создаёт EPUB из файла id и возвращает id результата. Отмена ctx прекращает
// только ожидание: начатая работа завершится, и следующий запрос получит её итог.
func (s *Service) Convert(ctx context.Context, id int64) (int64, error) {
	loc, err := s.store.FileLocation(ctx, id)
	if err != nil {
		return 0, err
	}
	if loc.Format != "fb2" && loc.Format != "fb2.zip" {
		return 0, ErrNotConvertible
	}
	ch := s.group.DoChan(loc.PartialMD5, func() (any, error) {
		file, err := s.run(id, loc)
		// Срок может выйти на любом шаге, в том числе при записи в базу.
		if errors.Is(err, context.DeadlineExceeded) {
			err = ErrTimeout
		}
		return file, err
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return 0, r.Err
		}
		return r.Val.(int64), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Delete убирает результат конвертации. Под семафором: иначе удаление каталога могло бы
// снести результат новой конвертации того же содержимого.
func (s *Service) Delete(ctx context.Context, id int64) error {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	dir, err := s.store.DeleteConverted(ctx, id)
	if err != nil {
		return err
	}
	// Строки в индексе уже нет: остаток на диске перезапишет следующая конвертация.
	if err := os.RemoveAll(dir); err != nil {
		s.log.Warn("library: remove converted file", "dir", dir, "error", err)
	}
	return nil
}

func (s *Service) run(id int64, loc library.FileLocation) (int64, error) {
	s.wg.Add(1)
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
	defer cancel()
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	// Последовательные запросы singleflight не схлопывает.
	if existing, err := s.store.ConvertedFile(ctx, loc.PartialMD5); err != nil || existing != 0 {
		return existing, err
	}
	// Каталог может остаться после падения сервера.
	dir := filepath.Join(s.converted, loc.PartialMD5)
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	file, err := s.produce(ctx, id, loc, dir)
	if err != nil {
		s.log.Warn("library: conversion failed", "file", id, "error", err)
		if rerr := os.RemoveAll(dir); rerr != nil {
			s.log.Warn("library: remove failed conversion", "dir", dir, "error", rerr)
		}
		return 0, err
	}
	s.log.Info("library: file converted", "source", id, "file", file)
	return file, nil
}

func (s *Service) produce(ctx context.Context, id int64, loc library.FileLocation, dir string) (int64, error) {
	tmp := filepath.Join(dir, tmpResult)
	if err := s.child(ctx, loc, tmp); err != nil {
		return 0, err
	}
	fi, err := os.Stat(tmp)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrFailed, err)
	}
	partial, err := hashing.PartialMD5(tmp)
	if err != nil {
		return 0, err
	}
	c := library.Converted{SourceID: id, SourceMD5: loc.PartialMD5, Size: fi.Size(), MTime: fi.ModTime().UnixNano(), PartialMD5: partial}
	if c.Meta, err = meta.Parse(tmp, "epub"); err != nil {
		c.Meta, c.MetaErr = meta.Book{}, err.Error()
	}
	name := ResultName(loc.Path, loc.Format)
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return 0, err
	}
	c.Path = loc.PartialMD5 + "/" + name
	c.Hashes = append([]string{partial}, hashing.FilenameMD5Variants(name)...)
	return s.store.AddConverted(ctx, c)
}

// ResultName — базовое имя источника с расширением .epub без нормализации Unicode:
// CrossPoint в режиме FILENAME хеширует имя файла на устройстве. Имя .fb2 длиной 255
// байт укорачивается по границе символа: .epub на байт длиннее.
func ResultName(rel, format string) string {
	base := path.Base(rel)
	stem := base[:len(base)-len(format)-1]
	for len(stem)+len(".epub") > maxName {
		_, n := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-n]
	}
	return stem + ".epub"
}

// child запускает конвертер в пустом рабочем каталоге с минимальным окружением:
// секреты сервера дочернему процессу не нужны.
func (s *Service) child(ctx context.Context, loc library.FileLocation, out string) error {
	work, err := os.MkdirTemp(s.temp, "job-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	var stderr limitedWriter
	cmd := exec.CommandContext(ctx, s.exe, "convert-fb2", loc.Root, loc.Path, loc.Format, out)
	cmd.Dir = work
	// fb2cng создаёт временные файлы через os.MkdirTemp(""), параметра каталога у него нет.
	cmd.Env = []string{"TMPDIR=" + work, "GOMEMLIMIT=" + memLimit}
	cmd.Stderr = &stderr
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case exitTooLarge:
			return meta.ErrTooLarge
		case exitSourceOpen:
			return fmt.Errorf("%w: %s", ErrUnavailable, stderr.buf)
		}
	}
	return fmt.Errorf("%w: %w: %s", ErrFailed, err, stderr.buf)
}

// limitedWriter хранит начало вывода, остальное отбрасывает.
type limitedWriter struct{ buf []byte }

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := maxStderr - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
