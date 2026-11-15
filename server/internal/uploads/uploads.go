// Package uploads принимает файлы книг в служебный корень загрузок: загрузку из панели
// и запись по WebDAV-диску /dav/books/. Файл сначала целиком пишется во временный файл
// рядом с целью и только потом получает своё имя, поэтому сканер не видит
// недописанных книг, а оборванная передача не оставляет следов.
package uploads

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxSize — предел размера одного файла.
const MaxSize int64 = 1 << 30

// tempPrefix начинается с точки: сканер и листинг книг такие имена пропускают.
const tempPrefix = ".ksb-upload-"

// staleTemp — возраст временного файла, после которого он считается брошенным
// (сервер убили посреди передачи).
const staleTemp = time.Hour

// maxName — предел длины имени файла в байтах у распространённых ФС.
const maxName = 255

var (
	// ErrTooLarge — тело больше предела.
	ErrTooLarge = errors.New("file is too large")
	// ErrInvalidName — имя не годится для файла в корне загрузок.
	ErrInvalidName = errors.New("invalid file name")
	// ErrNoFreeName — все кандидаты имени заняты.
	ErrNoFreeName = errors.New("no free file name")
)

// ReadError — ошибка чтения тела запроса (клиент оборвал передачу), а не записи на диск.
type ReadError struct{ Err error }

func (e ReadError) Error() string { return "read body: " + e.Err.Error() }
func (e ReadError) Unwrap() error { return e.Err }

// ValidName приводит имя к NFC и проверяет, что из него получится файл в корне
// загрузок: не пустое, не длиннее 255 байт, без точки в начале (такие файлы сканер
// не видит), без разделителей пути, NUL и управляющих символов.
func ValidName(name string) (string, error) {
	name = norm.NFC.String(name)
	switch {
	case name == "", len(name) > maxName, !utf8.ValidString(name), strings.HasPrefix(name, "."),
		strings.ContainsAny(name, `/\`):
		return "", ErrInvalidName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrInvalidName
		}
	}
	return name, nil
}

// maxCandidates — сколько суффиксов перебирается до отказа.
const maxCandidates = 1000

// candidates — name, затем «X (2).ext», «X (3).ext» и так далее: суффикс встаёт перед
// расширением книги format (для fb2.zip — перед .fb2.zip) с исходным регистром.
// Имя, которое с суффиксом вышло бы за 255 байт, укорачивается по символам.
func candidates(name, format string) func(yield func(string) bool) {
	base, ext := name, ""
	if format != "" && len(name) > len(format)+1 {
		base, ext = name[:len(name)-len(format)-1], name[len(name)-len(format)-1:]
	}
	return func(yield func(string) bool) {
		if !yield(name) {
			return
		}
		for i := 2; i <= maxCandidates; i++ {
			suffix := fmt.Sprintf(" (%d)", i)
			b := base
			for len(b)+len(suffix)+len(ext) > maxName && b != "" {
				_, size := utf8.DecodeLastRuneInString(b)
				b = b[:len(b)-size]
			}
			if !yield(b + suffix + ext) {
				return
			}
		}
	}
}

// Dir — корень загрузок. os.Root не выпускает за корень ни через «..», ни через симлинк.
type Dir struct {
	root *os.Root
}

// Open открывает каталог корня загрузок; каталог должен существовать.
func Open(dir string) (*Dir, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Dir{root: root}, nil
}

// Close закрывает корень.
func (d *Dir) Close() error { return d.root.Close() }

// Path — путь корня на диске.
func (d *Dir) Path() string { return d.root.Name() }

// Receive пишет body во временный файл в каталоге dir корня (путь через «/») и
// возвращает его путь и размер. Временный файл лежит рядом с целью, в той же ФС,
// поэтому переименование атомарно. Больше limit — ErrTooLarge, ошибка чтения тела —
// ReadError; при любой ошибке временный файл удаляется.
func (d *Dir) Receive(dir string, body io.Reader, limit int64) (tmp string, size int64, err error) {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	tmp = path.Join(dir, tempPrefix+hex.EncodeToString(buf[:]))
	f, err := d.root.OpenFile(filepath.FromSlash(tmp), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", 0, err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = d.root.Remove(filepath.FromSlash(tmp))
			tmp, size = "", 0
		}
	}()
	src := &errReader{r: io.LimitReader(body, limit+1)}
	size, err = io.Copy(f, src)
	switch {
	case src.err != nil:
		return tmp, size, ReadError{src.err}
	case err != nil:
		return tmp, size, err
	case size > limit:
		return tmp, size, ErrTooLarge
	}
	return tmp, size, f.Sync()
}

// errReader запоминает ошибку чтения, чтобы отличить обрыв передачи от ошибки записи.
type errReader struct {
	r   io.Reader
	err error
}

func (r *errReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

// Place даёт временному файлу свободное имя в корне: name или name с суффиксом
// « (N)» перед расширением книги format, и возвращает итоговое имя. Существующий
// файл не перезаписывается: link занимает имя атомарно, а rename молча заменил бы
// файл, появившийся между проверкой и переименованием.
func (d *Dir) Place(tmp, name, format string) (string, error) {
	defer func() { _ = d.root.Remove(filepath.FromSlash(tmp)) }()
	for c := range candidates(name, format) {
		err := d.root.Link(filepath.FromSlash(tmp), c)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", ErrNoFreeName
}

// Replace переименовывает временный файл в name (путь через «/»), заменяя
// существующий: семантика PUT.
func (d *Dir) Replace(tmp, name string) error {
	if err := d.root.Rename(filepath.FromSlash(tmp), filepath.FromSlash(name)); err != nil {
		_ = d.root.Remove(filepath.FromSlash(tmp))
		return err
	}
	return nil
}

// Remove удаляет файл name (путь через «/»). Файла уже нет — не ошибка.
func (d *Dir) Remove(name string) error {
	err := d.root.Remove(filepath.FromSlash(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Stat — сведения о файле name (путь через «/»).
func (d *Dir) Stat(name string) (fs.FileInfo, error) {
	return d.root.Stat(filepath.FromSlash(name))
}

// CleanTemp удаляет временные файлы старше часа во всём корне: следы передач,
// прерванных вместе с сервером. Более свежие могут принадлежать идущей передаче.
func (d *Dir) CleanTemp() error {
	cutoff := time.Now().Add(-staleTemp)
	return fs.WalkDir(d.root.FS(), ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), tempPrefix) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if err := d.root.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}
