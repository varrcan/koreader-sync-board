package library

import (
	"context"
	"database/sql"
	"errors"
)

// BookByHash — книга файла с содержимым partialMD5 или 0, если такого файла нет.
// Ищется по первичному ключу хешей; условие на partial_md5 отсекает совпадения по md5
// имени: одинаковое имя у разных книг — не дубликат.
func (s *Store) BookByHash(ctx context.Context, partialMD5 string) (int64, error) {
	var id int64
	err := s.db.Read.QueryRowContext(ctx, `
		SELECT f.book_id FROM library_file_hashes h JOIN library_files f ON f.id = h.file_id
		WHERE h.hash = ? AND f.partial_md5 = h.hash LIMIT 1`, partialMD5).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// FileKind — вид корня файла; нет в индексе — ErrFileNotFound.
func (s *Store) FileKind(ctx context.Context, id int64) (ManagedKind, error) {
	var kind ManagedKind
	err := s.db.Read.QueryRowContext(ctx, `
		SELECT r.managed FROM library_files f JOIN library_roots r ON r.id = f.root_id WHERE f.id = ?`, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrFileNotFound
	}
	return kind, err
}

// DeleteUploaded убирает загруженный файл из индекса и возвращает его путь
// относительно корня загрузок: файл с диска удаляет вызывающий, после фиксации.
// Сбой между шагами оставит файл на диске, и скан загрузок вернёт его в индекс;
// обратный порядок оставил бы запись о несуществующем файле. Прогресс привязан к
// хешам и не меняется. Файла нет — ErrFileNotFound, файл не из корня загрузок —
// ErrReadOnlyFile.
func (s *Store) DeleteUploaded(ctx context.Context, id int64) (string, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var (
		path    string
		managed ManagedKind
		bookID  int64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT f.path, r.managed, f.book_id
		FROM library_files f JOIN library_roots r ON r.id = f.root_id WHERE f.id = ?`, id).
		Scan(&path, &managed, &bookID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrFileNotFound
	}
	if err != nil {
		return "", err
	}
	if managed != ManagedUploads {
		return "", ErrReadOnlyFile
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_files WHERE id = ?`, id); err != nil {
		return "", err
	}
	if err := refreshBooks(ctx, tx, []int64{bookID}); err != nil {
		return "", err
	}
	return path, tx.Commit()
}
