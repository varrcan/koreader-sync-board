// Package library индексирует файлы книг в read-only корнях: хеши, по которым
// синхронизация находит файл, локальные метаданные и группировку файлов в книги.
package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

var (
	// ErrInvalidPath — путь корня не абсолютный, не каталог или не читается.
	// Ошибка оборачивается с причиной.
	ErrInvalidPath = errors.New("invalid path")
	// ErrRootExists — корень с тем же путём уже есть.
	ErrRootExists = errors.New("root already exists")
	// ErrRootOverlap — корень вложен в существующий или содержит его.
	ErrRootOverlap = errors.New("root overlaps an existing root")
	// ErrNotFound — корня нет.
	ErrNotFound = errors.New("root not found")
	// ErrBookNotFound — книги нет.
	ErrBookNotFound = errors.New("book not found")
	// ErrFileNotFound — файла нет в индексе.
	ErrFileNotFound = errors.New("file not found")
	// ErrSameBook — книгу сливают саму с собой.
	ErrSameBook = errors.New("cannot merge a book into itself")
	// ErrSingleFile — файл единственный в своей книге, отделять нечего.
	ErrSingleFile = errors.New("file is the only one in its book")
)

// Root — корень библиотеки.
type Root struct {
	ID        int64
	Name      string
	Path      string
	Files     int
	ScannedAt *time.Time
	Error     *string
}

// Store — таблицы библиотеки.
type Store struct {
	db  *store.DB
	now func() time.Time
}

// NewStore создаёт Store.
func NewStore(db *store.DB) *Store { return &Store{db: db, now: time.Now} }

const rootColumns = `r.id, r.name, r.path,
	(SELECT count(*) FROM library_files f WHERE f.root_id = r.id), r.scanned_at, r.error`

func scanRoot(row interface{ Scan(...any) error }) (Root, error) {
	var (
		r       Root
		scanned sql.NullInt64
		msg     sql.NullString
	)
	if err := row.Scan(&r.ID, &r.Name, &r.Path, &r.Files, &scanned, &msg); err != nil {
		return Root{}, err
	}
	if scanned.Valid {
		t := time.Unix(scanned.Int64, 0).UTC()
		r.ScannedAt = &t
	}
	if msg.Valid {
		r.Error = &msg.String
	}
	return r, nil
}

// Roots возвращает корни в порядке добавления.
func (s *Store) Roots(ctx context.Context) ([]Root, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT `+rootColumns+` FROM library_roots r ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Root
	for rows.Next() {
		r, err := scanRoot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Root возвращает корень по id или ErrNotFound.
func (s *Store) Root(ctx context.Context, id int64) (Root, error) {
	r, err := scanRoot(s.db.Read.QueryRowContext(ctx, `SELECT `+rootColumns+` FROM library_roots r WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Root{}, ErrNotFound
	}
	return r, err
}

// AddRoot проверяет путь и добавляет корень. Пустое name — последний элемент пути.
func (s *Store) AddRoot(ctx context.Context, path, name string) (Root, error) {
	if !filepath.IsAbs(path) {
		return Root{}, fmt.Errorf("%w: path must be absolute", ErrInvalidPath)
	}
	path = filepath.Clean(path)
	if err := checkDir(path); err != nil {
		return Root{}, err
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		return Root{}, err
	}
	resolved := resolve(path)
	// Пути существующих корней разыменовываются вне транзакции записи: недоступный
	// NAS может держать stat долго, а писатель общий с kosync.
	for _, r := range roots {
		if r.Path == path {
			return Root{}, ErrRootExists
		}
		if other := resolve(r.Path); within(resolved, other) || within(other, resolved) {
			return Root{}, fmt.Errorf("%w: %s", ErrRootOverlap, r.Path)
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(path)
	}
	res, err := s.db.Write.ExecContext(ctx, `
		INSERT INTO library_roots (name, path, created_at) VALUES (?, ?, ?)
		ON CONFLICT (path) DO NOTHING`, name, path, s.now().Unix())
	if err != nil {
		return Root{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Root{}, err
	} else if n == 0 {
		return Root{}, ErrRootExists
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Root{}, err
	}
	return Root{ID: id, Name: name, Path: path}, nil
}

// checkDir: path — существующий каталог, который процесс может прочитать.
func checkDir(path string) error {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %s does not exist", ErrInvalidPath, path)
	case err != nil:
		return fmt.Errorf("%w: %w", ErrInvalidPath, err)
	case !fi.IsDir():
		return fmt.Errorf("%w: %s is not a directory", ErrInvalidPath, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: cannot read %s: %w", ErrInvalidPath, path, err)
	}
	defer f.Close()
	if _, err := f.ReadDir(1); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: cannot read %s: %w", ErrInvalidPath, path, err)
	}
	return nil
}

// resolve разыменовывает симлинки пути; если не удаётся (NAS недоступен), путь как есть.
func resolve(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// within: path совпадает с dir или лежит внутри него.
func within(path, dir string) bool {
	if path == dir {
		return true
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return strings.HasPrefix(path, dir)
}

// DeleteRoot удаляет корень, его файлы и книги, оставшиеся без файлов. Файлы на
// диске не трогаются.
func (s *Store) DeleteRoot(ctx context.Context, id int64) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var files string
	if err := tx.QueryRowContext(ctx,
		`SELECT json_group_array(id) FROM library_files WHERE root_id = ?`, id).Scan(&files); err != nil {
		return err
	}
	books, err := fileBooks(ctx, tx, files)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM library_roots WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	// После слияния у книги бывают файлы из разных корней: метаданные могли браться
	// из удалённого.
	if err := refreshBooks(ctx, tx, books); err != nil {
		return err
	}
	return tx.Commit()
}

// counts — число книг, файлов и корней и конец последнего скана.
func (s *Store) counts(ctx context.Context) (books, files, roots int, finished *time.Time, err error) {
	var last sql.NullInt64
	err = s.db.Read.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM library_books), (SELECT count(*) FROM library_files),
		(SELECT count(*) FROM library_roots), (SELECT max(scanned_at) FROM library_roots)`).
		Scan(&books, &files, &roots, &last)
	if last.Valid {
		t := time.Unix(last.Int64, 0).UTC()
		finished = &t
	}
	return books, files, roots, finished, err
}

// known — запись индекса, по которой сканер решает, читать ли файл заново.
type known struct {
	id          int64
	size, mtime int64
}

func (s *Store) rootFiles(ctx context.Context, rootID int64) (map[string]known, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT id, path, size, mtime_ns FROM library_files WHERE root_id = ?`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]known{}
	for rows.Next() {
		var (
			k    known
			path string
		)
		if err := rows.Scan(&k.id, &path, &k.size, &k.mtime); err != nil {
			return nil, err
		}
		out[path] = k
	}
	return out, rows.Err()
}

// change — новый (id = 0) или изменённый файл с посчитанными хешами и метаданными.
type change struct {
	id          int64
	path        string
	size, mtime int64
	format      string
	partialMD5  string
	hashes      []string
	meta        meta.Book
	metaErr     string
}

// apply записывает пакет изменений корня в одной транзакции и пересчитывает
// затронутые книги: название появляется вместе с файлом, а книг без файлов не
// остаётся.
func (s *Store) apply(ctx context.Context, rootID int64, cs []change) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	var affected []int64
	for _, c := range cs {
		var (
			bookID int64
			pinned bool
		)
		if c.id != 0 {
			if err := tx.QueryRowContext(ctx, `SELECT book_id, pinned FROM library_files WHERE id = ?`, c.id).
				Scan(&bookID, &pinned); err != nil {
				return err
			}
			affected = append(affected, bookID)
		}
		// Книгу закреплённого файла задал человек: ключ её не переопределяет.
		if !pinned {
			if bookID, err = bookFor(ctx, tx, c.meta, now); err != nil {
				return err
			}
			affected = append(affected, bookID)
		}
		var authors string
		if len(c.meta.Authors) > 0 {
			authors = strings.Join(c.meta.Authors, ", ")
		}
		args := []any{c.size, c.mtime, c.format, c.partialMD5, bookID, c.meta.Title, authors,
			c.meta.Series, c.meta.SeriesIndex, c.meta.Language, c.metaErr, now}
		id := c.id
		if id == 0 {
			err = tx.QueryRowContext(ctx, `
				INSERT INTO library_files (size, mtime_ns, format, partial_md5, book_id, title, authors,
					series, series_index, language, meta_error, indexed_at, root_id, path)
				VALUES (?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, nullif(?, ''),
					nullif(?, ''), ?, ?, ?)
				RETURNING id`, append(args, rootID, c.path)...).Scan(&id)
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE library_files SET size = ?, mtime_ns = ?, format = ?, partial_md5 = ?, book_id = ?,
					title = nullif(?, ''), authors = nullif(?, ''), series = nullif(?, ''), series_index = ?,
					language = nullif(?, ''), meta_error = nullif(?, ''), indexed_at = ?, cover_checked = 0
				WHERE id = ?`, append(args, id)...)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM library_file_hashes WHERE file_id = ?`, id); err != nil {
			return err
		}
		for _, h := range c.hashes {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO library_file_hashes (hash, file_id) VALUES (?, ?)`, h, id); err != nil {
				return err
			}
		}
	}
	if err := refreshBooks(ctx, tx, affected); err != nil {
		return err
	}
	return tx.Commit()
}

// bookFor находит книгу по ключу группировки или создаёт новую. Файл без
// названия получает собственную книгу без ключа.
func bookFor(ctx context.Context, tx *sql.Tx, b meta.Book, now int64) (int64, error) {
	key, ok := meta.Key(b.Title, b.Authors)
	if ok {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT book_id FROM library_book_keys WHERE key = ?`, key).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	var id int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO library_books (created_at) VALUES (?) RETURNING id`, now).Scan(&id); err != nil {
		return 0, err
	}
	if ok {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO library_book_keys (key, book_id) VALUES (?, ?)`, key, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// removeFiles удаляет файлы из индекса и пересчитывает их книги.
func (s *Store) removeFiles(ctx context.Context, ids []int64) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	list, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	books, err := fileBooks(ctx, tx, string(list))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM library_files WHERE id IN (SELECT value FROM json_each(?))`, string(list)); err != nil {
		return err
	}
	if err := refreshBooks(ctx, tx, books); err != nil {
		return err
	}
	return tx.Commit()
}

// fileBooks — книги файлов из JSON-списка id.
func fileBooks(ctx context.Context, tx *sql.Tx, ids string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT book_id FROM library_files
		WHERE id IN (SELECT value FROM json_each(?))`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var books []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		books = append(books, id)
	}
	return books, rows.Err()
}

// refreshBooks пересчитывает метаданные книг: поле правки, иначе поле файла с
// наименьшим id среди файлов с названием. Пустая правка скрывает значение файла.
// Книги без файлов удаляются; ключи и правки уходят каскадом.
func refreshBooks(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE library_books SET (title, authors, series, series_index, language) = (
			SELECT nullif(coalesce(m.title, f.title), ''), nullif(coalesce(m.authors, f.authors), ''),
				nullif(coalesce(m.series, f.series), ''),
				CASE WHEN m.series IS NOT NULL THEN m.series_index ELSE f.series_index END,
				nullif(coalesce(m.language, f.language), '')
			FROM library_books b
			LEFT JOIN library_book_meta m ON m.book_id = b.id
			`+sourceFile+`
			WHERE b.id = library_books.id)
		WHERE id IN (SELECT value FROM json_each(?))`, string(list)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		DELETE FROM library_books WHERE id IN (SELECT value FROM json_each(?))
			AND NOT EXISTS (SELECT 1 FROM library_files f WHERE f.book_id = library_books.id)`, string(list))
	return err
}

// coverFile — файл, обложку которого предстоит проверить.
type coverFile struct {
	id                 int64
	root, path, format string
}

// uncheckedCovers — страница файлов с непроверенной обложкой. Форматы отбираются
// здесь, поэтому файлы остальных форматов не открываются.
func (s *Store) uncheckedCovers(ctx context.Context, after int64, limit int) ([]coverFile, error) {
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT f.id, r.path, f.path, f.format
		FROM library_files f JOIN library_roots r ON r.id = f.root_id
		WHERE f.cover_checked = 0 AND f.format IN ('epub', 'fb2', 'fb2.zip') AND f.id > ?
		ORDER BY f.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []coverFile
	for rows.Next() {
		var f coverFile
		if err := rows.Scan(&f.id, &f.root, &f.path, &f.format); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// setCover записывает итог проверки; cover == "" — обложки нет. По одному файлу:
// писатель в SQLite один, kosync не должен ждать пакет.
func (s *Store) setCover(ctx context.Context, id int64, cover string) error {
	_, err := s.db.Write.ExecContext(ctx,
		`UPDATE library_files SET cover = nullif(?, ''), cover_checked = 1 WHERE id = ?`, cover, id)
	return err
}

// usedCovers — миниатюры, на которые ссылаются файлы и правки книг.
func (s *Store) usedCovers(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT cover FROM library_files WHERE cover IS NOT NULL
		UNION SELECT cover FROM library_book_meta WHERE cover IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		used[id] = true
	}
	return used, rows.Err()
}

// finishRoot записывает итог скана корня; msg == "" — успех.
func (s *Store) finishRoot(ctx context.Context, rootID int64, msg string) error {
	_, err := s.db.Write.ExecContext(ctx,
		`UPDATE library_roots SET scanned_at = ?, error = nullif(?, '') WHERE id = ?`,
		s.now().Unix(), msg, rootID)
	return err
}
