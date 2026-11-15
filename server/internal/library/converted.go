package library

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

// ErrReadOnlyFile — файл лежит не в том корне, из которого его удаляют: в обычном
// корне сервер ничего не удаляет, а вид корня мог смениться между проверками.
var ErrReadOnlyFile = errors.New("file is read-only")

// Converted — готовый результат конвертации. Path — относительно служебного корня.
// SourceMD5 — partial MD5 содержимого, из которого он сделан: источник мог измениться,
// пока шла конвертация, поэтому значение передаётся, а не читается из строки источника.
type Converted struct {
	SourceID    int64
	SourceMD5   string
	Path        string
	Size, MTime int64
	PartialMD5  string
	Hashes      []string
	Meta        meta.Book
	MetaErr     string
}

// ConvertedFile — id результата конвертации содержимого sourceMD5 или 0, если его нет.
func (s *Store) ConvertedFile(ctx context.Context, sourceMD5 string) (int64, error) {
	var id int64
	err := s.db.Read.QueryRowContext(ctx, `SELECT id FROM library_files WHERE source_md5 = ?`, sourceMD5).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// AddConverted индексирует результат конвертации в книге источника и возвращает id
// файла. Источника уже нет — ErrFileNotFound. Обложка копируется вместе с признаком
// проверки: на диске это та же миниатюра, и сканеру незачем читать EPUB ради неё.
func (s *Store) AddConverted(ctx context.Context, c Converted) (int64, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var (
		bookID  int64
		cover   sql.NullString
		checked bool
	)
	err = tx.QueryRowContext(ctx, `SELECT book_id, cover, cover_checked FROM library_files WHERE id = ?`, c.SourceID).
		Scan(&bookID, &cover, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrFileNotFound
	}
	if err != nil {
		return 0, err
	}
	var id int64
	now := s.now().Unix()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO library_files (root_id, path, size, mtime_ns, format, partial_md5, book_id, title, authors,
			series, series_index, language, meta_error, indexed_at, pinned, cover, cover_checked, source_md5)
		VALUES ((SELECT id FROM library_roots WHERE managed = 1), ?, ?, ?, 'epub', ?, ?, nullif(?, ''), nullif(?, ''),
			nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?, 1, ?, ?, ?)
		RETURNING id`,
		c.Path, c.Size, c.MTime, c.PartialMD5, bookID, c.Meta.Title, strings.Join(c.Meta.Authors, ", "),
		c.Meta.Series, c.Meta.SeriesIndex, c.Meta.Language, c.MetaErr, now, cover, checked, c.SourceMD5).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err := addHashes(ctx, tx, id, c.Hashes); err != nil {
		return 0, err
	}
	if err := refreshBooks(ctx, tx, []int64{bookID}); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// DeleteConverted убирает результат конвертации из индекса и возвращает его каталог на
// диске: удаляет каталог вызывающий, после фиксации транзакции. Прогресс привязан к
// хешам и не меняется. Файла нет — ErrFileNotFound, файл не из корня конвертаций — ErrReadOnlyFile.
func (s *Store) DeleteConverted(ctx context.Context, id int64) (string, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var (
		root    string
		source  sql.NullString
		managed ManagedKind
		bookID  int64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT r.path, f.source_md5, r.managed, f.book_id
		FROM library_files f JOIN library_roots r ON r.id = f.root_id WHERE f.id = ?`, id).
		Scan(&root, &source, &managed, &bookID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrFileNotFound
	}
	if err != nil {
		return "", err
	}
	if managed != ManagedConverted || !source.Valid {
		return "", ErrReadOnlyFile
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_files WHERE id = ?`, id); err != nil {
		return "", err
	}
	// Книга могла состоять из одного этого файла: после разделения или пропажи источника.
	if err := refreshBooks(ctx, tx, []int64{bookID}); err != nil {
		return "", err
	}
	return filepath.Join(root, source.String), tx.Commit()
}
