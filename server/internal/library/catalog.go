package library

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
)

// lower() и LIKE в SQLite понимают только ASCII, поэтому поиск нормализует текст
// кодом Go. Функция видна соединениям, открытым после регистрации: init пакета
// срабатывает раньше store.Open.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("ksb_fold", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			s, _ := args[0].(string)
			return meta.Fold(s), nil
		})
}

// Book — книга библиотеки. Formats — различные форматы файлов в порядке их id,
// Filename — имя без каталогов файла с наименьшим id.
type Book struct {
	ID                     int64
	Title, Authors, Series *string
	SeriesIndex            *float64
	Language               *string
	Files                  int
	Formats                []string
	Filename               string
}

// BookPage — страница каталога. Next — курсор следующей страницы, 0 на последней.
type BookPage struct {
	Items []Book
	Next  int64
}

// FileProgress — последнее состояние прогресса пользователя по одному из хешей файла.
type FileProgress struct {
	Hash       string
	Percentage float64
	Finished   bool
	UpdatedAt  time.Time
}

// File — файл книги библиотеки. Progress — nil, если пользователь его не читал.
type File struct {
	ID             int64
	RootName, Path string
	Format         string
	Size           int64
	Title, Authors *string
	Pinned         bool
	Progress       *FileProgress
}

// BookDetail — книга и её файлы по возрастанию id.
type BookDetail struct {
	Book  Book
	Files []File
}

// FileLocation — где файл лежит на диске.
type FileLocation struct {
	Root, Path, Format string
}

// wordCond — слово запроса входит в метаданные книги или в путь одного из её файлов.
const wordCond = ` AND (instr(ksb_fold(concat_ws(' ', lb.title, lb.authors, lb.series)), ?) > 0
	OR EXISTS (SELECT 1 FROM library_files f WHERE f.book_id = lb.id AND instr(ksb_fold(f.path), ?) > 0))`

const bookSelect = `SELECT lb.id, lb.title, lb.authors, lb.series, lb.series_index, lb.language FROM library_books lb`

// Books возвращает страницу каталога от последней созданной книги к более ранним.
// before — курсор (0 — с начала). Каждое слово q ищется подстрокой, порядок слов
// не важен; пустой после нормализации q ничего не отбирает.
//
// Ключ и курсор — id книги: его выдаёт единственный писатель, поэтому книга,
// созданная во время листания, уходит выше курсора и повторно не приходит.
func (s *Store) Books(ctx context.Context, q string, before int64, limit int) (BookPage, error) {
	if before <= 0 {
		before = math.MaxInt64
	}
	query := bookSelect + ` WHERE lb.id < ?`
	args := []any{before}
	for _, word := range strings.Fields(meta.Fold(q)) {
		query += wordCond
		args = append(args, word, word)
	}
	query += ` ORDER BY lb.id DESC LIMIT ?`
	args = append(args, limit+1)

	// Книги и их файлы — из одного снимка, иначе слияние между запросами оставит
	// книгу без файлов.
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BookPage{}, err
	}
	defer tx.Rollback()
	books, err := queryBooks(ctx, tx, query, args...)
	if err != nil {
		return BookPage{}, err
	}
	page := BookPage{Items: books}
	if len(books) > limit {
		page.Items = books[:limit]
		page.Next = page.Items[limit-1].ID
	}
	return page, fillFiles(ctx, tx, page.Items)
}

// Book возвращает книгу, её файлы и прогресс по ним пользователя userID;
// несуществующая книга — ErrBookNotFound.
func (s *Store) Book(ctx context.Context, id, userID int64) (BookDetail, error) {
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BookDetail{}, err
	}
	defer tx.Rollback()
	books, err := queryBooks(ctx, tx, bookSelect+` WHERE lb.id = ?`, id)
	if err != nil {
		return BookDetail{}, err
	}
	if len(books) == 0 {
		return BookDetail{}, ErrBookNotFound
	}
	if err := fillFiles(ctx, tx, books); err != nil {
		return BookDetail{}, err
	}
	files, err := bookFiles(ctx, tx, id)
	if err != nil {
		return BookDetail{}, err
	}
	if err := fillProgress(ctx, tx, id, userID, files); err != nil {
		return BookDetail{}, err
	}
	return BookDetail{Book: books[0], Files: files}, nil
}

func queryBooks(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]Book, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	books := []Book{}
	for rows.Next() {
		var (
			b                                Book
			title, authors, series, language sql.NullString
			index                            sql.NullFloat64
		)
		if err := rows.Scan(&b.ID, &title, &authors, &series, &index, &language); err != nil {
			return nil, err
		}
		b.Title, b.Authors, b.Series, b.Language = nullable(title), nullable(authors), nullable(series), nullable(language)
		if index.Valid {
			b.SeriesIndex = &index.Float64
		}
		b.Formats = []string{}
		books = append(books, b)
	}
	return books, rows.Err()
}

// fillFiles заполняет Files, Formats и Filename одним запросом на все книги.
func fillFiles(ctx context.Context, tx *sql.Tx, books []Book) error {
	if len(books) == 0 {
		return nil
	}
	ids := make([]int64, len(books))
	index := make(map[int64]*Book, len(books))
	for i := range books {
		ids[i] = books[i].ID
		index[books[i].ID] = &books[i]
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT book_id, path, format FROM library_files
		WHERE book_id IN (SELECT value FROM json_each(?)) ORDER BY id`, string(list))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id           int64
			file, format string
		)
		if err := rows.Scan(&id, &file, &format); err != nil {
			return err
		}
		b := index[id]
		if b.Files == 0 {
			b.Filename = path.Base(file)
		}
		b.Files++
		if !slices.Contains(b.Formats, format) {
			b.Formats = append(b.Formats, format)
		}
	}
	return rows.Err()
}

func bookFiles(ctx context.Context, tx *sql.Tx, bookID int64) ([]File, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT f.id, r.name, f.path, f.format, f.size, f.title, f.authors, f.pinned
		FROM library_files f JOIN library_roots r ON r.id = f.root_id
		WHERE f.book_id = ? ORDER BY f.id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := []File{}
	for rows.Next() {
		var (
			f              File
			title, authors sql.NullString
		)
		if err := rows.Scan(&f.ID, &f.RootName, &f.Path, &f.Format, &f.Size, &title, &authors, &f.Pinned); err != nil {
			return nil, err
		}
		f.Title, f.Authors = nullable(title), nullable(authors)
		files = append(files, f)
	}
	return files, rows.Err()
}

// fillProgress находит прогресс пользователя по всем хешам каждого файла: читалка
// могла синхронизировать и partial MD5, и md5 имени. Берётся последний по журналу.
func fillProgress(ctx context.Context, tx *sql.Tx, bookID, userID int64, files []File) error {
	index := make(map[int64]*File, len(files))
	for i := range files {
		index[files[i].ID] = &files[i]
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT f.id, d.hash, e.percentage, e.server_ts
		FROM library_files f
		JOIN library_file_hashes h ON h.file_id = f.id
		JOIN documents d ON d.hash = h.hash
		JOIN progress_current c ON c.document_id = d.id AND c.user_id = ?
		JOIN progress_events e ON e.id = c.event_id
		WHERE f.book_id = ?
		ORDER BY e.id`, userID, bookID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, ts int64
			p      FileProgress
		)
		if err := rows.Scan(&id, &p.Hash, &p.Percentage, &ts); err != nil {
			return err
		}
		p.Finished = p.Percentage >= progress.FinishedAt
		p.UpdatedAt = time.Unix(ts, 0).UTC()
		if f := index[id]; f != nil {
			f.Progress = &p
		}
	}
	return rows.Err()
}

// FileLocation возвращает путь корня и относительный путь файла; нет в индексе —
// ErrFileNotFound.
func (s *Store) FileLocation(ctx context.Context, id int64) (FileLocation, error) {
	var l FileLocation
	err := s.db.Read.QueryRowContext(ctx, `
		SELECT r.path, f.path, f.format FROM library_files f
		JOIN library_roots r ON r.id = f.root_id WHERE f.id = ?`, id).Scan(&l.Root, &l.Path, &l.Format)
	if errors.Is(err, sql.ErrNoRows) {
		return FileLocation{}, ErrFileNotFound
	}
	return l, err
}

// Merge вливает книгу from в into: ключи и файлы переходят, файлы закрепляются,
// from удаляется. Прогресс привязан к хешам и не меняется.
func (s *Store) Merge(ctx context.Context, into, from int64) error {
	if into == from {
		return ErrSameBook
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM library_books WHERE id IN (?, ?)`, into, from).Scan(&found); err != nil {
		return err
	}
	if found != 2 {
		return ErrBookNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE library_book_keys SET book_id = ? WHERE book_id = ?`, into, from); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE library_files SET book_id = ?, pinned = 1 WHERE book_id = ?`, into, from); err != nil {
		return err
	}
	if err := refreshBooks(ctx, tx, []int64{into, from}); err != nil {
		return err
	}
	return tx.Commit()
}

// Split переносит файл в новую книгу без ключей и закрепляет за ней: новые файлы
// того же произведения по-прежнему идут в прежнюю книгу. Возвращает id новой книги.
func (s *Store) Split(ctx context.Context, fileID int64) (int64, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var old, files int64
	err = tx.QueryRowContext(ctx, `SELECT f.book_id, (SELECT count(*) FROM library_files o WHERE o.book_id = f.book_id)
		FROM library_files f WHERE f.id = ?`, fileID).Scan(&old, &files)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrFileNotFound
	}
	if err != nil {
		return 0, err
	}
	if files == 1 {
		return 0, ErrSingleFile
	}
	var id int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO library_books (created_at) VALUES (?) RETURNING id`, s.now().Unix()).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE library_files SET book_id = ?, pinned = 1 WHERE id = ?`, id, fileID); err != nil {
		return 0, err
	}
	if err := refreshBooks(ctx, tx, []int64{old, id}); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func nullable(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
