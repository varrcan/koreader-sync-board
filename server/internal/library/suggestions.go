package library

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Состояния фонового поиска метаданных книги (library_book_suggestions.state).
// Книга без строки ещё не проверялась.
const (
	SuggestComplete  = "complete"  // пробелов нет, искать нечего
	SuggestNone      = "none"      // искали, предложений нет
	SuggestFound     = "found"     // есть предложение, ждёт администратора
	SuggestFailed    = "failed"    // проверка не удалась, повтор через SuggestRetry
	SuggestDismissed = "dismissed" // предложение отклонено или просмотрено
)

// SuggestRetry — через сколько неудавшаяся проверка повторяется.
const SuggestRetry = 24 * time.Hour

// suggestQueue — книги, ждущие проверки: с названием, не проверенные или с давней
// неудачей, и с уже извлечёнными обложками файлов, иначе «нет обложки» было бы ложным.
const suggestQueue = `FROM library_books b
	LEFT JOIN library_book_suggestions s ON s.book_id = b.id
	WHERE b.title IS NOT NULL
		AND (s.book_id IS NULL OR (s.state = 'failed' AND s.checked_at <= ?))
		AND NOT EXISTS (SELECT 1 FROM library_files f WHERE f.book_id = b.id
			AND f.cover_checked = 0 AND f.format IN ('epub', 'fb2', 'fb2.zip'))`

// NextSuggestCandidate — следующая после after книга, ждущая проверки; 0 — таких
// нет. Неудавшиеся проверки не позднее failedBefore снова в очереди.
func (s *Store) NextSuggestCandidate(ctx context.Context, after int64, failedBefore time.Time) (int64, error) {
	var id int64
	err := s.db.Read.QueryRowContext(ctx, `SELECT b.id `+suggestQueue+` AND b.id > ? ORDER BY b.id LIMIT 1`,
		failedBefore.Unix(), after).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// SuggestPending — сколько книг ждёт проверки сейчас.
func (s *Store) SuggestPending(ctx context.Context) (int, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx, `SELECT count(*) `+suggestQueue, s.now().Add(-SuggestRetry).Unix()).Scan(&n)
	return n, err
}

// SuggestFound — сколько книг с предложением.
func (s *Store) SuggestFound(ctx context.Context) (int, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx, `SELECT count(*) FROM library_book_suggestions WHERE state = 'found'`).Scan(&n)
	return n, err
}

// RecordSuggestCheck записывает итог проверки книги. metaUpdated — время правки книги
// (Unix), прочитанное перед проверкой, 0 — правки не было. Проверка длится секунды, и
// запись условная: изменилась правка — книга останется в очереди и проверится с
// новыми данными; администратор успел отклонить — отклонение не перезаписывается.
// Книга, удалённая за это время, не пишется: SELECT без неё ничего не вставит.
func (s *Store) RecordSuggestCheck(ctx context.Context, bookID int64, state string, metaUpdated int64) error {
	_, err := s.db.Write.ExecContext(ctx, `
		INSERT INTO library_book_suggestions (book_id, state, checked_at)
		SELECT id, ?, ? FROM library_books
		WHERE id = ? AND coalesce((SELECT updated_at FROM library_book_meta WHERE book_id = ?), 0) = ?
		ON CONFLICT (book_id) DO UPDATE SET state = excluded.state, checked_at = excluded.checked_at
		WHERE library_book_suggestions.state = 'failed'`,
		state, s.now().Unix(), bookID, bookID, metaUpdated)
	return err
}

// DismissSuggestion снимает отметку предложения; книга больше не проверяется.
func (s *Store) DismissSuggestion(ctx context.Context, bookID int64) error {
	res, err := s.db.Write.ExecContext(ctx, `
		INSERT INTO library_book_suggestions (book_id, state, checked_at)
		SELECT id, 'dismissed', ? FROM library_books WHERE id = ?
		ON CONFLICT (book_id) DO UPDATE SET state = excluded.state, checked_at = excluded.checked_at`,
		s.now().Unix(), bookID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrBookNotFound
	}
	return nil
}

// viewedSuggestion — сохранение правки значит, что администратор посмотрел
// предложение. Книгу без отметки правка из очереди не убирает: исправленная опечатка
// в названии не должна лишать книгу поиска обложки.
func viewedSuggestion(ctx context.Context, tx *sql.Tx, bookID, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE library_book_suggestions SET state = 'dismissed', checked_at = ?
		WHERE book_id = ? AND state = 'found'`, now, bookID)
	return err
}
