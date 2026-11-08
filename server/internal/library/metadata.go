package library

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MetadataEdit — форма метаданных книги целиком. Пустая строка — поле очищено.
// Cover — новая миниатюра, ResetCover убирает прежнюю; без обоих она сохраняется.
type MetadataEdit struct {
	Title, Authors, Series, Language, Description string
	SeriesIndex                                   *float64
	Source                                        *string
	Cover                                         *string
	ResetCover                                    bool
}

// MetadataInfo — откуда взяты правки книги и когда сохранены.
type MetadataInfo struct {
	Source    *string
	UpdatedAt time.Time
}

// sourceFile присоединяет файл, из которого книга b берёт метаданные. LEFT JOIN:
// у книги из одних PDF такого файла нет, а правка применяться должна.
const sourceFile = `LEFT JOIN library_files f ON f.id = (
		SELECT x.id FROM library_files x WHERE x.book_id = b.id AND x.title IS NOT NULL ORDER BY x.id LIMIT 1)`

// SetMetadata заменяет правки книги. Поле, совпавшее с файлом, не хранится: копия
// перекрыла бы будущее исправление файла. Все файлы книги закрепляются, иначе
// перезаписанный файл с новым названием ушёл бы по ключу в другую книгу, а старая
// удалилась бы вместе с правкой.
func (s *Store) SetMetadata(ctx context.Context, id int64, e MetadataEdit) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var (
		title, authors, series, language sql.NullString
		index                            sql.NullFloat64
	)
	err = tx.QueryRowContext(ctx, `SELECT f.title, f.authors, f.series, f.series_index, f.language
		FROM library_books b `+sourceFile+` WHERE b.id = ?`, id).Scan(&title, &authors, &series, &index, &language)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBookNotFound
	}
	if err != nil {
		return err
	}
	cover := e.Cover
	if cover == nil && !e.ResetCover {
		var old sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT cover FROM library_book_meta WHERE book_id = ?`, id).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		cover = nullable(old)
	}

	editTitle, editAuthors, editLanguage := override(e.Title, title), override(e.Authors, authors), override(e.Language, language)
	// Серия и номер — пара: иначе нельзя было бы исправить только номер, а новая
	// серия получила бы номер из файла.
	var editSeries, editIndex any
	formIndex := e.SeriesIndex
	if e.Series == "" {
		formIndex = nil
	}
	if e.Series != series.String || !sameIndex(formIndex, index) {
		editSeries = e.Series
		if formIndex != nil {
			editIndex = *formIndex
		}
	}
	var description any
	if e.Description != "" {
		description = e.Description
	}

	if editTitle == nil && editAuthors == nil && editSeries == nil && editLanguage == nil && description == nil && cover == nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM library_book_meta WHERE book_id = ?`, id); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO library_book_meta
				(book_id, title, authors, series, series_index, language, description, cover, source, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, editTitle, editAuthors, editSeries, editIndex, editLanguage, description, cover, e.Source, s.now().Unix()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE library_files SET pinned = 1 WHERE book_id = ?`, id); err != nil {
			return err
		}
	}
	if err := refreshBooks(ctx, tx, []int64{id}); err != nil {
		return err
	}
	return tx.Commit()
}

// override — значение для слоя правок: NULL, если форма совпала с файлом.
func override(form string, file sql.NullString) any {
	if form == file.String {
		return nil
	}
	return form
}

func sameIndex(form *float64, file sql.NullFloat64) bool {
	if form == nil || !file.Valid {
		return form == nil && !file.Valid
	}
	return *form == file.Float64
}

// ClearMetadata удаляет все правки книги, включая обложку. Закрепление файлов
// остаётся: книгу файла по-прежнему определил человек.
func (s *Store) ClearMetadata(ctx context.Context, id int64) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM library_books WHERE id = ?`, id).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		return ErrBookNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_book_meta WHERE book_id = ?`, id); err != nil {
		return err
	}
	if err := refreshBooks(ctx, tx, []int64{id}); err != nil {
		return err
	}
	return tx.Commit()
}

// bookMetadata — аннотация и сведения о правках; у книги без правок оба nil.
func bookMetadata(ctx context.Context, tx *sql.Tx, id int64) (*string, *MetadataInfo, error) {
	var (
		description, source sql.NullString
		updated             int64
	)
	err := tx.QueryRowContext(ctx, `SELECT description, source, updated_at FROM library_book_meta WHERE book_id = ?`, id).
		Scan(&description, &source, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return nullable(description), &MetadataInfo{Source: nullable(source), UpdatedAt: time.Unix(updated, 0).UTC()}, nil
}
