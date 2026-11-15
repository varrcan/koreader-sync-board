package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

// formats — расширения файлов книг: подмножество провайдеров документов KOReader
// (.azw3 KOReader не регистрирует). txt, html и doc в каталогах книг чаще мусор.
var formats = []string{"fb2.zip", "epub", "fb2", "pdf", "djvu", "mobi", "azw", "cbz"}

// FormatOf возвращает формат файла по имени без учёта регистра.
func FormatOf(name string) (string, bool) {
	lower := strings.ToLower(name)
	for _, f := range formats {
		if strings.HasSuffix(lower, "."+f) {
			return f, true
		}
	}
	return "", false
}

const batchSize = 200

// Summary — сводка библиотеки и состояние скана.
type Summary struct {
	Books, Files, Roots int
	Running             bool
	StartedAt           *time.Time // начало текущего скана
	FinishedAt          *time.Time // конец последнего завершённого скана корня
}

// Scanner обходит корни в фоне: периодически, по Trigger и сразу после старта, а
// корень загрузок — ещё и по TriggerUploads. Одновременно идёт не больше одного скана.
type Scanner struct {
	store    *Store
	log      *slog.Logger
	interval time.Duration
	trigger  chan struct{} // полный скан
	uploads  chan struct{} // скан корня загрузок

	mu        sync.Mutex
	active    bool // скан идёт
	queued    bool // триггер ждёт, пока Run его заберёт
	startedAt time.Time

	walk  func(root string, fn fs.WalkDirFunc) error
	hash  func(path string) (string, error)
	parse func(path, format string) (meta.Book, error)

	covers *Covers // nil — обложки не извлекаются
	open   func(root, name string) (*os.File, error)
}

// Option настраивает Scanner.
type Option func(*Scanner)

// WithWalk подменяет обход каталога (filepath.WalkDir) — для тестов.
func WithWalk(walk func(root string, fn fs.WalkDirFunc) error) Option {
	return func(s *Scanner) { s.walk = walk }
}

// WithReader подменяет хеширование и разбор файла — для тестов.
func WithReader(hash func(string) (string, error), parse func(path, format string) (meta.Book, error)) Option {
	return func(s *Scanner) { s.hash, s.parse = hash, parse }
}

// WithCovers включает проход обложек с записью миниатюр в c.
func WithCovers(c *Covers) Option {
	return func(s *Scanner) { s.covers = c }
}

// NewScanner создаёт сканер. interval = 0 — скан только по Trigger.
func NewScanner(st *Store, interval time.Duration, log *slog.Logger, opts ...Option) *Scanner {
	s := &Scanner{
		store: st, log: log, interval: interval, trigger: make(chan struct{}, 1), uploads: make(chan struct{}, 1),
		walk: filepath.WalkDir, hash: hashing.PartialMD5, parse: meta.Parse, open: os.OpenInRoot,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Trigger ставит скан в очередь, не блокируясь. Сколько бы раз его ни вызвали во
// время скана, после него выполнится ровно один повторный.
func (s *Scanner) Trigger() { s.enqueue(s.trigger) }

// TriggerUploads ставит в очередь скан одного корня загрузок с теми же правилами
// слияния. Полный скан включает загрузки и поглощает его.
func (s *Scanner) TriggerUploads() { s.enqueue(s.uploads) }

func (s *Scanner) enqueue(ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = true
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Summary возвращает сводку. running учитывает и ещё не забранный триггер, чтобы
// ответ сразу после Trigger уже показывал скан.
func (s *Scanner) Summary(ctx context.Context) (Summary, error) {
	var (
		sum Summary
		err error
	)
	sum.Books, sum.Files, sum.Roots, sum.FinishedAt, err = s.store.counts(ctx)
	if err != nil {
		return Summary{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sum.Running = s.active || s.queued
	if s.active {
		t := s.startedAt.UTC()
		sum.StartedAt = &t
	}
	return sum, nil
}

// Run сканирует до отмены ctx: сразу и по таймеру, если interval > 0, и по Trigger;
// корень загрузок — по TriggerUploads.
func (s *Scanner) Run(ctx context.Context) {
	var tick <-chan time.Time
	if s.interval > 0 {
		t := time.NewTicker(s.interval)
		defer t.Stop()
		tick = t.C
		s.Trigger()
	}
	for {
		full := true
		select {
		case <-ctx.Done():
			return
		case <-s.trigger:
		case <-tick:
		case <-s.uploads:
			full = false
		}
		// select из двух готовых каналов выбирает случайный, поэтому поглощение — в обе
		// стороны: полный скан в очереди выполняется вместо скана загрузок, а запрос
		// скана загрузок учитывается полным. Под mu, чтобы триггер, пришедший после
		// select, не потерял queued.
		s.mu.Lock()
		select {
		case <-s.trigger:
			full = true
		default:
		}
		select {
		case <-s.uploads:
		default:
		}
		s.queued, s.active, s.startedAt = false, true, time.Now()
		s.mu.Unlock()
		if full {
			s.scanAll(ctx)
		} else {
			s.scanUploads(ctx)
		}
		s.mu.Lock()
		s.active = false
		s.mu.Unlock()
	}
}

// scanAll сканирует корни последовательно в порядке id.
func (s *Scanner) scanAll(ctx context.Context) {
	roots, err := s.store.scanRoots(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("library: scan failed", "error", err)
		}
		return
	}
	for _, r := range roots {
		if ctx.Err() != nil {
			return
		}
		s.scanRoot(ctx, r)
	}
	if ctx.Err() == nil {
		s.extractCovers(ctx, 0)
	}
}

// scanUploads сканирует только корень загрузок: новая книга видна быстро, без обхода
// корней на NAS.
func (s *Scanner) scanUploads(ctx context.Context) {
	root, err := s.store.uploadsRoot(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("library: uploads scan failed", "error", err)
		}
		return
	}
	s.scanRoot(ctx, root)
	if ctx.Err() == nil {
		s.extractCovers(ctx, root.ID)
	}
}

// extractCovers читает обложки файлов, которые ещё не проверены: новых, изменённых
// и проиндексированных до появления обложек. Идёт после обхода корней, чтобы
// чтение FB2 целиком не задерживало названия и хеши; scan.running остаётся true.
// rootID != 0 — только файлы этого корня и без уборки каталога: скан загрузок не
// должен открывать файлы отключённого NAS (на hard-mount NFS он бы повис), а
// осиротевших миниатюр после него не бывает.
func (s *Scanner) extractCovers(ctx context.Context, rootID int64) {
	if s.covers == nil {
		return
	}
	var (
		start                 = time.Now()
		after                 int64
		checked, found, retry int
	)
	warn := func(msg string, err error) {
		if ctx.Err() == nil {
			s.log.Warn(msg, "error", err)
		}
	}
	for {
		files, err := s.store.uncheckedCovers(ctx, rootID, after, batchSize)
		if err != nil {
			warn("library: covers failed", err)
			return
		}
		if len(files) == 0 {
			break
		}
		for _, f := range files {
			if ctx.Err() != nil {
				return
			}
			after = f.id
			src, err := s.readCover(f)
			var pe *fs.PathError
			if errors.As(err, &pe) {
				// Корень отключён или EIO: файл останется непроверенным до следующего скана.
				retry++
				s.log.Debug("library: cover unavailable", "path", f.path, "error", err)
				continue
			}
			var id string
			if err != nil {
				s.log.Debug("library: cover", "path", f.path, "error", err)
			} else if src != nil {
				thumb, err := Thumbnail(src)
				if err != nil {
					s.log.Debug("library: cover", "path", f.path, "error", err)
				} else if id, err = s.covers.save(thumb); err != nil {
					// Диск заполнен: без уборки, остальным файлам будет то же самое.
					warn("library: cover write failed", err)
					return
				}
			}
			if err := s.store.setCover(ctx, f.id, id); err != nil {
				warn("library: covers failed", err)
				return
			}
			checked++
			if id != "" {
				found++
			}
		}
	}
	if rootID == 0 {
		used := func() (map[string]bool, error) { return s.store.usedCovers(ctx) }
		if err := s.covers.sweep(used); err != nil {
			warn("library: covers cleanup failed", err)
		}
	}
	s.log.Info("library: covers finished", "checked", checked, "found", found, "retry", retry,
		"duration", time.Since(start).Round(time.Millisecond))
}

// readCover открывает файл только внутри корня: между обходом и проходом его могли
// подменить симлинком.
func (s *Scanner) readCover(f coverFile) ([]byte, error) {
	h, err := s.open(f.root, filepath.FromSlash(f.path))
	if err != nil {
		return nil, err
	}
	defer h.Close()
	fi, err := h.Stat()
	if err != nil {
		return nil, err
	}
	return meta.Cover(h, fi.Size(), f.format)
}

// errRootGone — корень удалили во время скана.
var errRootGone = errors.New("root deleted during scan")

// rootScan — состояние скана одного корня.
type rootScan struct {
	s       *Scanner
	root    Root
	dir     string // разыменованный путь корня
	known   map[string]known
	seen    map[string]bool
	partial []string // относительные каталоги, обойти которые не удалось
	errs    []string
	pending []change
	found   int

	added, updated, removed int
}

func (s *Scanner) scanRoot(ctx context.Context, root Root) {
	start := time.Now()
	rs := &rootScan{s: s, root: root, seen: map[string]bool{}}
	err := rs.run(ctx)
	switch {
	case ctx.Err() != nil:
		// Отмена: недообойдённое не отличить от удалённого, итог не пишется.
		return
	case errors.Is(err, errRootGone):
		return
	case err != nil:
		rs.errs = append([]string{err.Error()}, rs.errs...)
	}
	msg := summarize(rs.errs)
	if err := s.store.finishRoot(ctx, root.ID, msg); err != nil {
		s.log.Warn("library: scan failed", "root", root.Path, "error", err)
		return
	}
	if msg != "" {
		s.log.Warn("library: scan failed", "root", root.Path, "error", msg)
	}
	s.log.Info("library: scan finished", "root", root.Path, "files", rs.found,
		"added", rs.added, "updated", rs.updated, "removed", rs.removed,
		"duration", time.Since(start).Round(time.Millisecond))
}

// summarize — первая ошибка и число остальных.
func summarize(errs []string) string {
	switch len(errs) {
	case 0:
		return ""
	case 1:
		return errs[0]
	}
	return fmt.Sprintf("%s (and %d more)", errs[0], len(errs)-1)
}

// run обходит корень и пишет изменения. Ошибка означает, что индекс корня не
// изменён целиком (корень недоступен или пуст) или запись не удалась.
func (rs *rootScan) run(ctx context.Context) error {
	// WalkDir делает Lstat корня и в корень-симлинк не заходит: без разыменования
	// такой корень «успешно» сканировался бы как пустой.
	dir, err := filepath.EvalSymlinks(rs.root.Path)
	if err != nil {
		return fmt.Errorf("root unavailable: %w", err)
	}
	if fi, err := os.Stat(dir); err != nil {
		return fmt.Errorf("root unavailable: %w", err)
	} else if !fi.IsDir() {
		return fmt.Errorf("root unavailable: %s is not a directory", rs.root.Path)
	}
	rs.dir = dir
	if rs.known, err = rs.s.store.rootFiles(ctx, rs.root.ID); err != nil {
		return err
	}

	if err := rs.s.walk(dir, rs.visit(ctx)); err != nil {
		return err
	}
	// Пустая точка монтирования неотключённого NAS читается без ошибок: без этой
	// проверки скан стёр бы индекс вместе с ключами слияний. Каталог загрузок
	// принадлежит серверу: в нём пустота — это удалённый последний файл.
	if rs.found == 0 && len(rs.known) > 0 && rs.root.Managed != ManagedUploads {
		return errors.New("root is empty: not mounted? Delete the root to clear its index")
	}
	if err := rs.flush(ctx); err != nil {
		return err
	}
	var gone []int64
	for path, k := range rs.known {
		if !rs.seen[path] && !rs.underPartial(path) {
			gone = append(gone, k.id)
		}
	}
	if len(gone) > 0 {
		if err := rs.s.store.removeFiles(ctx, gone); err != nil {
			return err
		}
		rs.removed = len(gone)
	}
	return nil
}

// rootError — ошибка самого корня: прерывает обход до любых изменений.
type rootError struct{ err error }

func (e rootError) Error() string { return "root unavailable: " + e.err.Error() }
func (e rootError) Unwrap() error { return e.err }

func (rs *rootScan) visit(ctx context.Context) fs.WalkDirFunc {
	return func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if path == rs.dir {
				return rootError{err}
			}
			// Недоступный подкаталог: его файлы остаются в индексе.
			rs.partial = append(rs.partial, rs.rel(path))
			rs.errs = append(rs.errs, err.Error())
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == rs.dir {
			return nil
		}
		name := d.Name()
		// «.»: ._* от macOS, .Trash, .stfolder; *.sdr — метаданные KOReader.
		if strings.HasPrefix(name, ".") || (d.IsDir() && strings.HasSuffix(strings.ToLower(name), ".sdr")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Симлинки не разыменовываются: ни циклов, ни выхода за корень.
		if !d.Type().IsRegular() {
			return nil
		}
		format, ok := FormatOf(name)
		if !ok {
			return nil
		}
		rel := rs.rel(path)
		info, err := d.Info()
		if err == nil && info.Size() == 0 {
			// Так выглядит файл, который WebDAV-клиент создал до записи содержимого
			// (LOCK, нулевой PUT Windows). Проиндексированный раньше файл уходит из индекса.
			return nil
		}
		rs.found++
		rs.seen[rel] = true
		if err != nil {
			rs.errs = append(rs.errs, err.Error())
			return nil
		}
		size, mtime := info.Size(), info.ModTime().UnixNano()
		k, exists := rs.known[rel]
		if exists && k.size == size && k.mtime == mtime {
			return nil
		}
		// Ошибка чтения файла (EIO на NFS) не роняет скан: файл остаётся с
		// прежними данными, новый не добавляется.
		sum, err := rs.s.hash(path)
		if err != nil {
			rs.errs = append(rs.errs, err.Error())
			return nil
		}
		c := change{id: k.id, path: rel, size: size, mtime: mtime, format: format, partialMD5: sum,
			hashes: append([]string{sum}, hashing.FilenameMD5Variants(name)...)}
		if c.meta, err = rs.s.parse(path, format); err != nil {
			c.meta, c.metaErr = meta.Book{}, err.Error()
			rs.s.log.Debug("library: metadata", "path", path, "error", err)
		}
		rs.pending = append(rs.pending, c)
		if exists {
			rs.updated++
		} else {
			rs.added++
		}
		if len(rs.pending) >= batchSize {
			return rs.flush(ctx)
		}
		return nil
	}
}

// flush пишет накопленный пакет. Корень, удалённый во время скана, ломает вставку
// на внешнем ключе: тогда скан корня молча прекращается.
func (rs *rootScan) flush(ctx context.Context) error {
	if len(rs.pending) == 0 {
		return nil
	}
	if err := rs.s.store.apply(ctx, rs.root.ID, rs.pending); err != nil {
		if _, rerr := rs.s.store.Root(ctx, rs.root.ID); errors.Is(rerr, ErrNotFound) {
			return errRootGone
		}
		return err
	}
	rs.pending = rs.pending[:0]
	return nil
}

func (rs *rootScan) rel(path string) string {
	rel, err := filepath.Rel(rs.dir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func (rs *rootScan) underPartial(path string) bool {
	for _, p := range rs.partial {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
