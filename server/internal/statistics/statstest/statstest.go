// Package statstest собирает statistics.sqlite3 так, как его создаёт KOReader:
// схема из statistics.koplugin/main.lua (createDB), режим WAL и закрытие базы,
// после которого в заголовке остаётся признак WAL (байты 18–19 = 2).
package statstest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // драйвер "sqlite"
)

// Schema — createDB из statistics.koplugin/main.lua, DB_SCHEMA_VERSION = 20221111.
const Schema = `
	CREATE TABLE IF NOT EXISTS book
		(
			id integer PRIMARY KEY autoincrement,
			title text,
			authors text,
			notes      integer,
			last_open  integer,
			highlights integer,
			pages      integer,
			series text,
			language text,
			md5 text,
			total_read_time  integer,
			total_read_pages integer
		);
	CREATE UNIQUE INDEX IF NOT EXISTS book_title_authors_md5 ON book(title, authors, md5);
	CREATE TABLE IF NOT EXISTS page_stat_data
		(
			id_book     integer,
			page        integer NOT NULL DEFAULT 0,
			start_time  integer NOT NULL DEFAULT 0,
			duration    integer NOT NULL DEFAULT 0,
			total_pages integer NOT NULL DEFAULT 0,
			UNIQUE (id_book, page, start_time),
			FOREIGN KEY(id_book) REFERENCES book(id)
		);
	CREATE INDEX IF NOT EXISTS page_stat_data_start_time ON page_stat_data(start_time);
	CREATE TABLE IF NOT EXISTS numbers
		(
			number INTEGER PRIMARY KEY
		);
	WITH RECURSIVE counter AS
		(
			SELECT 1 as N UNION ALL
			SELECT N + 1 FROM counter WHERE N < 1000
		)
		INSERT INTO numbers SELECT N AS number FROM counter;
	CREATE VIEW IF NOT EXISTS page_stat AS
		SELECT id_book, first_page + idx - 1 AS page, start_time, duration / (last_page - first_page + 1) AS duration
		FROM (
			SELECT id_book, page, total_pages, pages, start_time, duration,
				((page - 1) * pages) / total_pages + 1 AS first_page,
				max(((page - 1) * pages) / total_pages + 1, (page * pages) / total_pages) AS last_page,
				idx
			FROM page_stat_data
			JOIN book ON book.id = id_book
			JOIN (SELECT number as idx FROM numbers) AS N ON idx <= (last_page - first_page + 1)
		);
	PRAGMA user_version=20221111;
`

// Book — строка book. Пустой MD5 записывается как NULL; остальные строки — как
// есть, плейсхолдер "N/A" задаёт сам тест.
type Book struct {
	ID                  int64
	Title, Authors, MD5 string
	Series, Language    string
	Pages               int64
	LastOpen            int64
	Records             []Record
}

// Record — строка page_stat_data.
type Record struct {
	Page, StartTime, Duration, TotalPages int64
}

// Path создаёт базу в каталоге теста и возвращает путь к файлу. extra
// выполняется после вставки книг.
func Path(tb testing.TB, books []Book, extra ...string) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "statistics.sqlite3")
	Write(tb, path, books, extra...)
	return path
}

// Build собирает базу и возвращает её байты.
func Build(tb testing.TB, books []Book, extra ...string) []byte {
	tb.Helper()
	data, err := os.ReadFile(Path(tb, books, extra...))
	if err != nil {
		tb.Fatal(err)
	}
	return data
}

// Write создаёт базу по пути path.
func Write(tb testing.TB, path string, books []Book, extra ...string) {
	tb.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		tb.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	exec := func(q string, args ...any) {
		tb.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			db.Close()
			tb.Fatalf("%s: %v", q, err)
		}
	}
	exec(`PRAGMA journal_mode=WAL`)
	exec(Schema)
	for _, b := range books {
		var md5 any
		if b.MD5 != "" {
			md5 = b.MD5
		}
		exec(`INSERT INTO book (id, title, authors, notes, last_open, highlights, pages, series, language, md5,
			total_read_time, total_read_pages) VALUES (?, ?, ?, 0, ?, 0, ?, ?, ?, ?, 0, 0)`,
			b.ID, b.Title, b.Authors, b.LastOpen, b.Pages, b.Series, b.Language, md5)
		for _, r := range b.Records {
			exec(`INSERT INTO page_stat_data (id_book, page, start_time, duration, total_pages) VALUES (?, ?, ?, ?, ?)`,
				b.ID, r.Page, r.StartTime, r.Duration, r.TotalPages)
		}
	}
	exec(`UPDATE book SET (total_read_pages, total_read_time) =
		(SELECT count(DISTINCT page), sum(duration) FROM page_stat WHERE id_book = book.id)`)
	for _, q := range extra {
		exec(q)
	}
	if err := db.Close(); err != nil {
		tb.Fatal(err)
	}
}
