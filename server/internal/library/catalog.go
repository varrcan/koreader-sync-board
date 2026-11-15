package library

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
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
// Filename — имя без каталогов файла с наименьшим id, Cover — миниатюра правки,
// иначе первого файла с обложкой.
type Book struct {
	ID                     int64
	Title, Authors, Series *string
	SeriesIndex            *float64
	Language               *string
	Files                  int
	Formats                []string
	Filename               string
	Cover                  *string
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
// Converted — файл создан конвертацией, Uploaded — лежит в корне загрузок,
// Convertible — это FB2, и результата для его содержимого нет.
type File struct {
	ID                               int64
	RootName, Path                   string
	Format                           string
	Size                             int64
	Title, Authors                   *string
	Pinned                           bool
	Converted, Uploaded, Convertible bool
	Progress                         *FileProgress
}

// BookDetail — книга и её файлы по возрастанию id. Description и Metadata — nil,
// если их нет в правках.
type BookDetail struct {
	Book        Book
	Files       []File
	Description *string
	Metadata    *MetadataInfo
}

// FileLocation — где файл лежит на диске и его partial MD5.
type FileLocation struct {
	Root, Path, Format, PartialMD5 string
}

// wordCond — слово запроса входит в метаданные книги или в путь одного из её файлов.
// У сконвертированного файла каталог — хеш (32 символа и «/»): его шестнадцатеричные
// цифры находились бы по словам вроде «face» и «2012».
const wordCond = ` AND (instr(ksb_fold(concat_ws(' ', lb.title, lb.authors, lb.series)), ?) > 0
	OR EXISTS (SELECT 1 FROM library_files f WHERE f.book_id = lb.id
		AND instr(ksb_fold(iif(f.source_md5 IS NULL, f.path, substr(f.path, 34))), ?) > 0))`

const bookSelect = `SELECT lb.id, lb.title, lb.authors, lb.series, lb.series_index, lb.language,
	(SELECT m.cover FROM library_book_meta m WHERE m.book_id = lb.id) FROM library_books lb`

// Books возвращает страницу каталога от последней созданной книги к более ранним.
// before — курсор (0 — с начала). Каждое слово q ищется подстрокой, порядок слов
// не важен; пустой после нормализации q ничего не отбирает.
//
// Ключ и курсор — id книги: его выдаёт единственный писатель, поэтому книга,
// созданная во время листания, уходит выше курсора и повторно не приходит.
func (s *Store) Books(ctx context.Context, q string, before int64, limit int) (BookPage, error) {
	// Книги и их файлы — из одного снимка, иначе слияние между запросами оставит
	// книгу без файлов.
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BookPage{}, err
	}
	defer tx.Rollback()
	return queryBookPage(ctx, tx, q, before, limit)
}

func queryBookPage(ctx context.Context, tx *sql.Tx, q string, before int64, limit int) (BookPage, error) {
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
	description, info, err := bookMetadata(ctx, tx, id)
	if err != nil {
		return BookDetail{}, err
	}
	return BookDetail{Book: books[0], Files: files, Description: description, Metadata: info}, nil
}

// FeedFile — файл книги в ленте каталога для читалок.
type FeedFile struct {
	ID           int64
	Path, Format string
	Size         int64
}

// FeedBook — книга ленты: файлы по возрастанию id, описание из правок и время
// последнего изменения.
type FeedBook struct {
	Book        Book
	Files       []FeedFile
	Description *string
	Updated     time.Time
}

// FeedPage — страница ленты. Next — курсор следующей страницы, 0 на последней.
type FeedPage struct {
	Items []FeedBook
	Next  int64
}

// Feed — страница книг с теми же правилами отбора и порядком, что Books, но с
// данными файлов, которые нужны записи каталога.
func (s *Store) Feed(ctx context.Context, q string, before int64, limit int) (FeedPage, error) {
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return FeedPage{}, err
	}
	defer tx.Rollback()
	return feedPage(ctx, tx, q, before, limit)
}

func feedPage(ctx context.Context, tx *sql.Tx, q string, before int64, limit int) (FeedPage, error) {
	page, err := queryBookPage(ctx, tx, q, before, limit)
	if err != nil {
		return FeedPage{}, err
	}
	out := FeedPage{Items: make([]FeedBook, len(page.Items)), Next: page.Next}
	if len(page.Items) == 0 {
		return out, nil
	}
	ids := make([]int64, len(page.Items))
	index := make(map[int64]*FeedBook, len(page.Items))
	for i, b := range page.Items {
		ids[i] = b.ID
		out.Items[i].Book = b
		index[b.ID] = &out.Items[i]
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return FeedPage{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT lb.id, m.description, coalesce(m.updated_at, lb.created_at), f.id, f.path, f.format, f.size
		FROM library_files f
		JOIN library_books lb ON lb.id = f.book_id
		LEFT JOIN library_book_meta m ON m.book_id = lb.id
		WHERE lb.id IN (SELECT value FROM json_each(?)) ORDER BY f.id`, string(list))
	if err != nil {
		return FeedPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, updated int64
			description sql.NullString
			f           FeedFile
		)
		if err := rows.Scan(&id, &description, &updated, &f.ID, &f.Path, &f.Format, &f.Size); err != nil {
			return FeedPage{}, err
		}
		b := index[id]
		b.Description = nullable(description)
		b.Updated = time.Unix(updated, 0).UTC()
		b.Files = append(b.Files, f)
	}
	return out, rows.Err()
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
			b                                       Book
			title, authors, series, language, cover sql.NullString
			index                                   sql.NullFloat64
		)
		if err := rows.Scan(&b.ID, &title, &authors, &series, &index, &language, &cover); err != nil {
			return nil, err
		}
		b.Title, b.Authors, b.Series, b.Language = nullable(title), nullable(authors), nullable(series), nullable(language)
		b.Cover = nullable(cover)
		if index.Valid {
			b.SeriesIndex = &index.Float64
		}
		b.Formats = []string{}
		books = append(books, b)
	}
	return books, rows.Err()
}

// fillFiles заполняет Files, Formats, Filename и Cover одним запросом на все книги.
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
	rows, err := tx.QueryContext(ctx, `SELECT book_id, path, format, cover FROM library_files
		WHERE book_id IN (SELECT value FROM json_each(?)) ORDER BY id`, string(list))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id           int64
			file, format string
			cover        sql.NullString
		)
		if err := rows.Scan(&id, &file, &format, &cover); err != nil {
			return err
		}
		b := index[id]
		if b.Cover == nil {
			b.Cover = nullable(cover)
		}
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
		SELECT f.id, r.name, f.path, f.format, f.size, f.title, f.authors, f.pinned, r.managed = 1, r.managed = 2,
			f.format IN ('fb2', 'fb2.zip')
				AND NOT EXISTS (SELECT 1 FROM library_files c WHERE c.source_md5 = f.partial_md5)
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
		if err := rows.Scan(&f.ID, &f.RootName, &f.Path, &f.Format, &f.Size, &title, &authors, &f.Pinned,
			&f.Converted, &f.Uploaded, &f.Convertible); err != nil {
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
		SELECT r.path, f.path, f.format, f.partial_md5 FROM library_files f
		JOIN library_roots r ON r.id = f.root_id WHERE f.id = ?`, id).Scan(&l.Root, &l.Path, &l.Format, &l.PartialMD5)
	if errors.Is(err, sql.ErrNoRows) {
		return FileLocation{}, ErrFileNotFound
	}
	return l, err
}

// OpenFile открывает файл библиотеки для отдачи. os.OpenInRoot не выпускает за
// корень ни через «..», ни через симлинк, подменённый после скана.
func OpenFile(l FileLocation) (*os.File, fs.FileInfo, error) {
	f, err := os.OpenInRoot(l.Root, filepath.FromSlash(l.Path))
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = errors.New("not a regular file")
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// Merge вливает книгу from в into: ключи и файлы переходят, файлы закрепляются,
// from удаляется. Правки into остаются, а если их нет — переходят правки from,
// иначе ручная работа исчезла бы вместе с источником. Прогресс привязан к хешам и
// не меняется.
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
	res, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO library_book_meta
			(book_id, title, authors, series, series_index, language, description, cover, source, updated_at)
		SELECT ?, title, authors, series, series_index, language, description, cover, source, updated_at
		FROM library_book_meta WHERE book_id = ?`, into, from)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		// Правки держат закрепление всех файлов книги, в том числе файлов приёмника.
		if _, err := tx.ExecContext(ctx, `UPDATE library_files SET pinned = 1 WHERE book_id = ?`, into); err != nil {
			return err
		}
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
