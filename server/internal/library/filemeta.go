package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

// maxMetadataFiles — сколько файлов книги читается при поиске кандидатов.
const maxMetadataFiles = 5

// ParseableFiles — файлы книги с разбираемыми метаданными по возрастанию id.
func (s *Store) ParseableFiles(ctx context.Context, bookID int64) ([]FileLocation, error) {
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT r.path, f.path, f.format FROM library_files f
		JOIN library_roots r ON r.id = f.root_id
		WHERE f.book_id = ? AND f.format IN ('epub', 'fb2', 'fb2.zip')
		ORDER BY f.id LIMIT ?`, bookID, maxMetadataFiles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileLocation
	for rows.Next() {
		var l FileLocation
		if err := rows.Scan(&l.Root, &l.Path, &l.Format); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// FileReader читает метаданные файлов книги в момент поиска. ISBN и аннотация не
// хранятся в индексе: иначе пришлось бы перечитать всю библиотеку на NAS ради
// редких ручных поисков.
type FileReader struct {
	Budget time.Duration
	Open   func(root, name string) (*os.File, error)
}

// NewFileReader создаёт FileReader с бюджетом 3 с и открытием только внутри корня.
func NewFileReader() FileReader { return FileReader{Budget: 3 * time.Second, Open: os.OpenInRoot} }

// Read объединяет метаданные файлов: основные поля — из первого файла с названием,
// ISBN и аннотация — первые найденные. Недоступные файлы пропускаются.
func (r FileReader) Read(files []FileLocation) meta.Book {
	b, _ := r.ReadAll(files)
	return b
}

// ReadAll — Read с признаком полноты. Чтение неполно, если файл не открылся или не
// прочитался (*fs.PathError: корень отключён, EIO) либо вышел бюджет: «нет аннотации»
// тогда может быть ложным. Ошибка разбора полноту не нарушает — повтор дал бы то же.
//
// Чтение идёт в горутине: системные вызовы на зависшем NFS контекстом не прерываются.
// По истечении бюджета возвращается то, что успели прочитать, а горутина завершится,
// когда вернётся системный вызов.
func (r FileReader) ReadAll(files []FileLocation) (meta.Book, bool) {
	type parsed struct {
		book meta.Book
		err  error
	}
	results := make(chan parsed, len(files))
	go func() {
		defer close(results)
		for _, f := range files {
			b, err := r.parse(f)
			results <- parsed{b, err}
		}
	}()
	timeout := time.NewTimer(r.Budget)
	defer timeout.Stop()
	var out meta.Book
	complete := true
	for {
		select {
		case <-timeout.C:
			return out, false
		case p, ok := <-results:
			if !ok {
				return out, complete
			}
			var pe *fs.PathError
			switch {
			case p.err == nil:
				merge(&out, p.book)
			case errors.As(p.err, &pe):
				complete = false
			}
		}
	}
}

func (r FileReader) parse(l FileLocation) (meta.Book, error) {
	f, err := r.Open(l.Root, filepath.FromSlash(l.Path))
	if err != nil {
		return meta.Book{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return meta.Book{}, err
	}
	if !fi.Mode().IsRegular() {
		return meta.Book{}, errors.New("not a regular file")
	}
	return meta.ParseAt(f, fi.Size(), l.Format)
}

func merge(out *meta.Book, b meta.Book) {
	if out.Title == "" && strings.TrimSpace(b.Title) != "" {
		out.Title, out.Authors, out.Series, out.SeriesIndex, out.Language = b.Title, b.Authors, b.Series, b.SeriesIndex, b.Language
	}
	if out.ISBN == "" {
		out.ISBN = b.ISBN
	}
	if out.Description == "" {
		out.Description = b.Description
	}
}
