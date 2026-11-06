package statistics

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
)

// importAttempts — сколько раз объединение повторяется, если снимок менялся.
const importAttempts = 3

// mergeSQL — объединение из ReaderStatistics.onSync (statistics.koplugin/main.lua)
// без блоков удаления: книги по (title, authors, md5), записи по (книга, страница,
// start_time) с большей длительностью, пересчёт итогов книги через вьюху page_stat.
// Удаления нужны клиенту, у которого есть прошлая выгрузка (.sync); при импорте
// её нет, и новый снимок должен включать прежний целиком.
const mergeSQL = `
	UPDATE book AS b
	SET last_open = i.last_open
	FROM income.book AS i
	WHERE (b.title, b.authors, b.md5) = (i.title, i.authors, i.md5)
	  AND i.last_open > b.last_open;

	INSERT INTO book (
		title, authors, notes, last_open, highlights, pages, series, language, md5, total_read_time, total_read_pages
	) SELECT
		title, authors, notes, last_open, highlights, pages, series, language, md5, total_read_time, total_read_pages
	FROM income.book
	WHERE (title, authors, md5) NOT IN (
		SELECT title, authors, md5 FROM book
	);

	CREATE TEMP TABLE book_id_map AS
		SELECT m.id AS mid, i.id AS iid FROM book m
		INNER JOIN income.book i
		ON (m.title, m.authors, m.md5) = (i.title, i.authors, i.md5);

	INSERT INTO page_stat_data (id_book, page, start_time, duration, total_pages)
		SELECT map.mid, page, start_time, duration, total_pages
		FROM income.page_stat_data
		INNER JOIN book_id_map AS map
		ON id_book = map.iid
		WHERE map.mid IS NOT null
	ON CONFLICT(id_book, page, start_time) DO UPDATE SET
	duration = MAX(duration, excluded.duration);

	UPDATE book SET (total_read_pages, total_read_time) =
	(SELECT count(DISTINCT page),
	        sum(duration)
	 FROM   page_stat
	 WHERE  id_book = book.id);

	DROP TABLE temp.book_id_map;
`

// Import принимает statistics.sqlite3, загруженный из панели. Снимка нет — файл
// становится снимком. Снимок есть — файл объединяется с ним без удалений: иначе
// читалка увидела бы в своей прошлой выгрузке книги, которых нет в новом снимке,
// и удалила бы их у себя. Если снимок изменился во время объединения (пришла
// синхронизация с читалки), объединение повторяется.
func (s *Store) Import(ctx context.Context, userID int64, content []byte) (File, error) {
	if _, err := s.check(ctx, userID, FileStatistics, content); err != nil {
		return File{}, err
	}
	for range importAttempts {
		data, cond := content, Cond{}
		cur, err := s.Get(ctx, userID, FileStatistics)
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return File{}, err
		default:
			data, err = s.merge(ctx, cur.Content, content)
			if errors.Is(err, ErrInvalid) {
				s.log.Warn("statistics: import rejected", "user_id", userID, "reason", err.Error())
			}
			if err != nil {
				return File{}, err
			}
			cond = Cond{Match: []string{cur.ETag}}
		}
		if s.beforeImportSave != nil {
			s.beforeImportSave()
		}
		f, _, err := s.Save(ctx, userID, nil, FileStatistics, data, cond)
		if errors.Is(err, ErrPrecondition) {
			continue
		}
		return f, err
	}
	return File{}, ErrConflict
}

// merge объединяет income с текущим снимком и возвращает новую базу. Текущий
// снимок открывается в памяти как main; income пишется во временный файл и
// подключается только для чтения: Deserialize работает лишь со схемой main.
func (s *Store) merge(ctx context.Context, current, income []byte) ([]byte, error) {
	m, err := openMemory(ctx, current)
	if err != nil {
		return nil, err
	}
	defer m.Close()

	f, err := os.CreateTemp(s.tmpDir, "ksb-import-*.sqlite3")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(legacyHeader(income))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}

	// immutable: файл никто не меняет, SQLite не нужны блокировки и журнал рядом с ним.
	uri := (&url.URL{Scheme: "file", Path: f.Name(), RawQuery: "mode=ro&immutable=1"}).String()
	if _, err := m.conn.ExecContext(ctx, `ATTACH DATABASE ? AS income`, uri); err != nil {
		return nil, fmt.Errorf("attach income: %w", err)
	}
	if _, err := m.conn.ExecContext(ctx, mergeSQL); err != nil {
		return nil, invalid("merge: %v", err)
	}
	if _, err := m.conn.ExecContext(ctx, `DETACH DATABASE income`); err != nil {
		return nil, err
	}
	return m.serialize()
}
