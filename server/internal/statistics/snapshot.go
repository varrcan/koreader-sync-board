// Package statistics хранит файлы облачной синхронизации KOReader и разбирает
// statistics.sqlite3 в статистику пользователя.
//
// Снимок открывается в памяти через Deserialize, без временных файлов. KOReader
// ведёт базу в режиме WAL, и в заголовке выгруженного файла байты 18–19 равны 2:
// такой файл без -wal/-shm не открывается («unable to open database file»), поэтому
// в копии буфера они заменяются на 1 (обычный журнал). Сам файл хранится и
// отдаётся без изменений.
package statistics

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite" // драйвер "sqlite"
)

// Имена файлов, которые KOReader синхронизирует через Cloud:sync.
const (
	FileStatistics = "statistics.sqlite3"
	FileVocabulary = "vocabulary_builder.sqlite3"
)

// MaxSize — предел размера принимаемого файла.
const MaxSize = 64 << 20

// ErrInvalid — файл не SQLite или не статистика KOReader. Оборачивает причину.
var ErrInvalid = errors.New("invalid statistics file")

var sqliteMagic = []byte("SQLite format 3\x00")

// Allowed сообщает, принимается ли файл с таким именем.
func Allowed(name string) bool {
	return name == FileStatistics || name == FileVocabulary
}

// Snapshot — разобранный statistics.sqlite3.
type Snapshot struct {
	Books []Book
	// Orphans — записи page_stat_data, чей id_book не указывает на книгу.
	Orphans int
}

// Book — строка book. authors, series и language без плейсхолдера "N/A" KOReader.
type Book struct {
	Title, Authors, Series, Language, MD5 sql.NullString

	Pages, LastOpen, Notes, Highlights, TotalReadTime, TotalReadPages sql.NullInt64

	Records []Record
}

// Record — строка page_stat_data.
type Record struct {
	Page, StartTime, Duration, TotalPages int64
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// checkSQLite проверяет сигнатуру базы SQLite.
func checkSQLite(content []byte) error {
	if !bytes.HasPrefix(content, sqliteMagic) {
		return invalid("not an SQLite database")
	}
	return nil
}

// legacyHeader возвращает копию content, в которой режим WAL в заголовке заменён
// на обычный журнал.
func legacyHeader(content []byte) []byte {
	buf := bytes.Clone(content)
	if len(buf) >= 20 && buf[18] == 2 && buf[19] == 2 {
		buf[18], buf[19] = 1, 1
	}
	return buf
}

// memDB — база в памяти на одном соединении. Пул закрывается вместе с ней, чтобы
// соединение с чужой базой не вернулось в оборот.
type memDB struct {
	pool *sql.DB
	conn *sql.Conn
}

func (m *memDB) Close() error {
	return errors.Join(m.conn.Close(), m.pool.Close())
}

// openMemory загружает content в базу в памяти. Ошибки открытия — ErrInvalid.
func openMemory(ctx context.Context, content []byte) (*memDB, error) {
	if err := checkSQLite(content); err != nil {
		return nil, err
	}
	pool, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	conn, err := pool.Conn(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	m := &memDB{pool: pool, conn: conn}
	buf := legacyHeader(content)
	err = conn.Raw(func(dc any) error {
		d, ok := dc.(interface{ Deserialize([]byte) error })
		if !ok {
			return errors.New("sqlite driver does not support Deserialize")
		}
		return d.Deserialize(buf)
	})
	if err != nil {
		m.Close()
		return nil, invalid("deserialize: %v", err)
	}
	return m, nil
}

func (m *memDB) serialize() ([]byte, error) {
	var out []byte
	err := m.conn.Raw(func(dc any) error {
		s, ok := dc.(interface{ Serialize() ([]byte, error) })
		if !ok {
			return errors.New("sqlite driver does not support Serialize")
		}
		var err error
		out, err = s.Serialize()
		return err
	})
	return out, err
}

// quickCheck — PRAGMA quick_check должен вернуть единственную строку "ok".
func (m *memDB) quickCheck(ctx context.Context) error {
	var res string
	if err := m.conn.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&res); err != nil {
		return invalid("quick_check: %v", err)
	}
	if res != "ok" {
		return invalid("quick_check: %s", res)
	}
	return nil
}

// Parse проверяет statistics.sqlite3 и читает книги и записи чтения. Колонки
// выбираются явно: лишние игнорируются, отсутствие нужной — ErrInvalid.
func Parse(ctx context.Context, content []byte) (*Snapshot, error) {
	m, err := openMemory(ctx, content)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	if err := m.quickCheck(ctx); err != nil {
		return nil, err
	}

	// Числа приводятся к INTEGER: типы колонок в SQLite не строгие, а значение
	// вроде 12.0 не сканируется в int64.
	rows, err := m.conn.QueryContext(ctx, `
		SELECT id, title,
		       nullif(nullif(authors, 'N/A'), ''),
		       nullif(nullif(series, 'N/A'), ''),
		       nullif(nullif(language, 'N/A'), ''),
		       nullif(md5, ''),
		       CAST(pages AS INTEGER), CAST(last_open AS INTEGER), CAST(notes AS INTEGER),
		       CAST(highlights AS INTEGER), CAST(total_read_time AS INTEGER), CAST(total_read_pages AS INTEGER)
		FROM book ORDER BY id`)
	if err != nil {
		return nil, invalid("book: %v", err)
	}
	defer rows.Close()
	snap := &Snapshot{}
	index := map[int64]int{}
	for rows.Next() {
		var id int64
		var b Book
		if err := rows.Scan(&id, &b.Title, &b.Authors, &b.Series, &b.Language, &b.MD5,
			&b.Pages, &b.LastOpen, &b.Notes, &b.Highlights, &b.TotalReadTime, &b.TotalReadPages); err != nil {
			return nil, invalid("book: %v", err)
		}
		index[id] = len(snap.Books)
		snap.Books = append(snap.Books, b)
	}
	if err := rows.Err(); err != nil {
		return nil, invalid("book: %v", err)
	}

	recs, err := m.conn.QueryContext(ctx, `
		SELECT id_book, CAST(page AS INTEGER), CAST(start_time AS INTEGER),
		       CAST(duration AS INTEGER), CAST(total_pages AS INTEGER)
		FROM page_stat_data ORDER BY rowid`)
	if err != nil {
		return nil, invalid("page_stat_data: %v", err)
	}
	defer recs.Close()
	for recs.Next() {
		var idBook sql.NullInt64
		var r Record
		var page, start, duration, total sql.NullInt64
		if err := recs.Scan(&idBook, &page, &start, &duration, &total); err != nil {
			return nil, invalid("page_stat_data: %v", err)
		}
		i, ok := index[idBook.Int64]
		if !idBook.Valid || !ok {
			snap.Orphans++
			continue
		}
		r.Page, r.StartTime, r.Duration, r.TotalPages = page.Int64, start.Int64, duration.Int64, total.Int64
		snap.Books[i].Records = append(snap.Books[i].Records, r)
	}
	if err := recs.Err(); err != nil {
		return nil, invalid("page_stat_data: %v", err)
	}
	return snap, nil
}

// Records возвращает общее число записей чтения.
func (s *Snapshot) Records() int {
	n := 0
	for _, b := range s.Books {
		n += len(b.Records)
	}
	return n
}
