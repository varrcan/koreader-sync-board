package statistics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

var (
	// ErrNotFound — у пользователя нет файла с таким именем.
	ErrNotFound = errors.New("file not found")
	// ErrPrecondition — условие записи не выполнено, ничего не записано.
	ErrPrecondition = errors.New("precondition failed")
	// ErrConflict — снимок менялся во время каждой попытки объединения.
	ErrConflict = errors.New("statistics changed during import")
)

// Источник файла.
const (
	SourceWebDAV = "webdav"
	SourceImport = "import"
)

// File — сохранённый файл. Content заполняет только Get.
type File struct {
	Name       string
	Content    []byte
	ETag       string
	Size       int64
	ModifiedAt time.Time
}

// Cond — условие записи. Match == nil: файла ещё нет. Иначе ETag текущего файла
// совпадает с одним из Match по строгому сравнению; "*" — любой существующий файл.
type Cond struct {
	Match []string
}

func (c Cond) ok(etag string, exists bool) bool {
	if c.Match == nil {
		return !exists
	}
	return exists && (slices.Contains(c.Match, "*") || slices.Contains(c.Match, etag))
}

// Store — файлы облачной синхронизации и статистика пользователей.
type Store struct {
	db     *store.DB
	log    *slog.Logger
	tmpDir string
	now    func() time.Time

	beforeImportSave func()
}

// Option настраивает Store.
type Option func(*Store)

// WithClock подменяет источник времени.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithBeforeImportSave вызывает f между объединением и записью импорта (для тестов).
func WithBeforeImportSave(f func()) Option { return func(s *Store) { s.beforeImportSave = f } }

// New создаёт Store. tmpDir — каталог для временного файла при объединении.
func New(db *store.DB, tmpDir string, log *slog.Logger, opts ...Option) *Store {
	s := &Store{db: db, log: log, tmpDir: tmpDir, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ETag — сильный ETag содержимого: первые 128 бит sha256 в кавычках.
func ETag(content []byte) string {
	sum := sha256.Sum256(content)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// Get возвращает файл пользователя вместе с содержимым.
func (s *Store) Get(ctx context.Context, userID int64, name string) (File, error) {
	f := File{Name: name}
	var modified int64
	err := s.db.Read.QueryRowContext(ctx, `
		SELECT content, etag, size, modified_at FROM webdav_files WHERE user_id = ? AND name = ?`,
		userID, name).Scan(&f.Content, &f.ETag, &f.Size, &modified)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	f.ModifiedAt = time.Unix(modified, 0).UTC()
	return f, nil
}

// List возвращает файлы пользователя без содержимого, по имени.
func (s *Store) List(ctx context.Context, userID int64) ([]File, error) {
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT name, etag, size, modified_at FROM webdav_files WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		var modified int64
		if err := rows.Scan(&f.Name, &f.ETag, &f.Size, &modified); err != nil {
			return nil, err
		}
		f.ModifiedAt = time.Unix(modified, 0).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// Save проверяет файл и записывает его, если выполнено cond. device — устройство,
// выгрузившее файл по WebDAV; nil — загрузка из панели. statistics.sqlite3 целиком
// заменяет статистику пользователя в той же транзакции.
//
// Ошибки: ErrInvalid (файл не принят, ничего не записано), ErrPrecondition,
// accounts.ErrUnauthorized (устройство отозвано после проверки входа). created —
// файла до записи не было.
func (s *Store) Save(ctx context.Context, userID int64, device *accounts.Principal, name string, content []byte, cond Cond) (File, bool, error) {
	if !Allowed(name) {
		return File{}, false, invalid("file name %q is not accepted", name)
	}
	snap, err := s.check(ctx, userID, name, content)
	if err != nil {
		return File{}, false, err
	}

	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return File{}, false, err
	}
	defer tx.Rollback()

	var credentialID any
	source := SourceImport
	if device != nil {
		if err := accounts.CheckTx(ctx, tx, *device); err != nil {
			return File{}, false, err
		}
		credentialID, source = device.CredentialID, SourceWebDAV
	}

	var etag string
	err = tx.QueryRowContext(ctx, `SELECT etag FROM webdav_files WHERE user_id = ? AND name = ?`,
		userID, name).Scan(&etag)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return File{}, false, err
	}
	if !cond.ok(etag, exists) {
		return File{}, false, ErrPrecondition
	}

	f := File{Name: name, ETag: ETag(content), Size: int64(len(content)), ModifiedAt: s.now().UTC().Truncate(time.Second)}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO webdav_files (user_id, name, content, etag, size, modified_at, credential_id, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, name) DO UPDATE SET
			content = excluded.content, etag = excluded.etag, size = excluded.size,
			modified_at = excluded.modified_at, credential_id = excluded.credential_id, source = excluded.source`,
		userID, name, content, f.ETag, f.Size, f.ModifiedAt.Unix(), credentialID, source); err != nil {
		return File{}, false, err
	}
	if snap != nil {
		if err := replace(ctx, tx, userID, snap, f.ModifiedAt.Unix()); err != nil {
			return File{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return File{}, false, err
	}
	return f, !exists, nil
}

// check проверяет файл до транзакции; для статистики возвращает разобранный снимок.
// Отказ пишется в лог с причиной.
func (s *Store) check(ctx context.Context, userID int64, name string, content []byte) (*Snapshot, error) {
	var snap *Snapshot
	err := checkSQLite(content)
	if err == nil && name == FileStatistics {
		snap, err = Parse(ctx, content)
	}
	if errors.Is(err, ErrInvalid) {
		s.log.Warn("statistics: file rejected", "user_id", userID, "file", name, "size", len(content), "reason", err.Error())
	}
	if err != nil {
		return nil, err
	}
	if snap != nil && snap.Orphans > 0 {
		s.log.Warn("statistics: page records without book skipped", "user_id", userID, "count", snap.Orphans)
	}
	return snap, nil
}

// replace заменяет статистику пользователя снимком. Записи страниц старых книг
// удаляются каскадом.
func replace(ctx context.Context, tx *sql.Tx, userID int64, snap *Snapshot, now int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM stat_books WHERE user_id = ?`, userID); err != nil {
		return err
	}
	regDoc, err := tx.PrepareContext(ctx,
		`INSERT INTO documents (hash, first_seen_at) VALUES (?, ?) ON CONFLICT (hash) DO NOTHING`)
	if err != nil {
		return err
	}
	defer regDoc.Close()
	docID, err := tx.PrepareContext(ctx, `SELECT id FROM documents WHERE hash = ?`)
	if err != nil {
		return err
	}
	defer docID.Close()
	insBook, err := tx.PrepareContext(ctx, `
		INSERT INTO stat_books (user_id, document_id, title, authors, series, language, pages, last_open,
			notes, highlights, total_read_time, total_read_pages)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insBook.Close()
	insPage, err := tx.PrepareContext(ctx, `
		INSERT INTO stat_pages (book_id, user_id, page, start_time, duration, total_pages)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insPage.Close()

	for _, b := range snap.Books {
		var doc sql.NullInt64
		if b.MD5.Valid {
			if _, err := regDoc.ExecContext(ctx, b.MD5.String, now); err != nil {
				return err
			}
			if err := docID.QueryRowContext(ctx, b.MD5.String).Scan(&doc); err != nil {
				return err
			}
		}
		res, err := insBook.ExecContext(ctx, userID, doc, b.Title, b.Authors, b.Series, b.Language,
			b.Pages, b.LastOpen, b.Notes, b.Highlights, b.TotalReadTime, b.TotalReadPages)
		if err != nil {
			return err
		}
		bookID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, r := range b.Records {
			if _, err := insPage.ExecContext(ctx, bookID, userID, r.Page, r.StartTime, r.Duration, r.TotalPages); err != nil {
				return err
			}
		}
	}
	return nil
}

// Status — состояние снимка статистики для панели.
type Status struct {
	UploadedAt time.Time
	// DeviceName — устройство, выгрузившее снимок; nil для импорта.
	DeviceName              *string
	Source                  string
	Size                    int64
	Books, Records          int
	FirstReadAt, LastReadAt *time.Time
}

// Status возвращает состояние снимка statistics.sqlite3 пользователя; снимка
// нет — nil.
func (s *Store) Status(ctx context.Context, userID int64) (*Status, error) {
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var st Status
	var uploaded int64
	var device sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT f.modified_at, cr.name, f.source, f.size
		FROM webdav_files f LEFT JOIN device_credentials cr ON cr.id = f.credential_id
		WHERE f.user_id = ? AND f.name = ?`, userID, FileStatistics).Scan(&uploaded, &device, &st.Source, &st.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st.UploadedAt = time.Unix(uploaded, 0).UTC()
	if device.Valid {
		st.DeviceName = &device.String
	}
	var first, last sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM stat_books WHERE user_id = ?1), count(*), min(start_time), max(start_time)
		FROM stat_pages WHERE user_id = ?1`, userID).Scan(&st.Books, &st.Records, &first, &last); err != nil {
		return nil, err
	}
	st.FirstReadAt, st.LastReadAt = optTime(first), optTime(last)
	return &st, nil
}

func optTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}
