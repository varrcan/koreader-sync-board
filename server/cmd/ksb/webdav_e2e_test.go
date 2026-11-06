package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/statistics"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics/statstest"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

// cloudReader притворяется KOReader со статистикой: Cloud:sync из
// cloudstorage.koplugin/main.lua и ReaderStatistics.onSync из statistics.koplugin.
// local — база читалки, рядом с ней .sync (прошлая выгрузка) и .temp (скачанный файл).
type cloudReader struct {
	t              *testing.T
	base           string
	user, password string
	local          string
}

func (c cloudReader) request(method string, body []byte, etag string) *http.Response {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+"/dav/"+statistics.FileStatistics, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.SetBasicAuth(c.user, c.password)
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return res
}

// download — WebDavApi.downloadFile: код ответа и ETag, тело — в .temp.
func (c cloudReader) download() (int, string) {
	c.t.Helper()
	income := c.local + ".temp"
	os.Remove(income)
	res := c.request("GET", nil, "")
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		data, err := io.ReadAll(res.Body)
		if err != nil {
			c.t.Fatal(err)
		}
		if err := os.WriteFile(income, data, 0o600); err != nil {
			c.t.Fatal(err)
		}
	}
	return res.StatusCode, res.Header.Get("ETag")
}

// upload — WebDavApi.uploadFile: If-Match только если ETag был.
func (c cloudReader) upload(etag string) int {
	c.t.Helper()
	data, err := os.ReadFile(c.local)
	if err != nil {
		c.t.Fatal(err)
	}
	res := c.request("PUT", data, etag)
	res.Body.Close()
	return res.StatusCode
}

// sync — Cloud:sync: GET → слияние → PUT, пока PUT отвечает 412. between
// вызывается после первого GET (чтобы вклинить чужую синхронизацию).
func (c cloudReader) sync(between func()) int {
	c.t.Helper()
	puts := 0
	for code := http.StatusPreconditionFailed; code == http.StatusPreconditionFailed; {
		got, etag := c.download()
		if got != http.StatusOK && got != http.StatusNotFound {
			c.t.Fatalf("GET: %d", got)
		}
		if got == http.StatusOK && etag == "" {
			c.t.Fatal("GET without ETag: KOReader would loop on 412 forever")
		}
		if between != nil {
			between()
			between = nil
		}
		onSync(c.t, c.local, c.local+".sync", c.local+".temp")
		code = c.upload(etag)
		puts++
		if puts > 5 {
			c.t.Fatal("sync loop does not converge")
		}
	}
	os.Remove(c.local + ".temp")
	data, err := os.ReadFile(c.local)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(c.local+".sync", data, 0o600); err != nil {
		c.t.Fatal(err)
	}
	return puts
}

// onSync — ReaderStatistics.onSync: при отсутствии income ничего не делает,
// при наличии .sync сначала распространяет удаления, затем объединяет.
func onSync(t *testing.T, local, cached, income string) {
	t.Helper()
	if _, err := os.Stat(income); err != nil {
		return
	}
	q := "ATTACH '" + income + "' AS income_db;"
	_, cacheErr := os.Stat(cached)
	if cacheErr == nil {
		q += "ATTACH '" + cached + `' AS cached_db;
			DELETE FROM income_db.page_stat_data WHERE id_book IN (
				SELECT id FROM income_db.book WHERE (title, authors, md5) IN (
					SELECT title, authors, md5 FROM cached_db.book WHERE (title, authors, md5) NOT IN (
						SELECT title, authors, md5 FROM book)));
			DELETE FROM income_db.book WHERE (title, authors, md5) IN (
				SELECT title, authors, md5 FROM cached_db.book WHERE (title, authors, md5) NOT IN (
					SELECT title, authors, md5 FROM book));
			DELETE FROM page_stat_data WHERE id_book IN (
				SELECT id FROM book WHERE (title, authors, md5) IN (
					SELECT title, authors, md5 FROM cached_db.book WHERE (title, authors, md5) NOT IN (
						SELECT title, authors, md5 FROM income_db.book)));
			DELETE FROM book WHERE (title, authors, md5) IN (
				SELECT title, authors, md5 FROM cached_db.book WHERE (title, authors, md5) NOT IN (
					SELECT title, authors, md5 FROM income_db.book));`
	}
	q += `
		UPDATE book AS b SET last_open = i.last_open FROM income_db.book AS i
		WHERE (b.title, b.authors, b.md5) = (i.title, i.authors, i.md5) AND i.last_open > b.last_open;
		INSERT INTO book (title, authors, notes, last_open, highlights, pages, series, language, md5, total_read_time, total_read_pages)
		SELECT title, authors, notes, last_open, highlights, pages, series, language, md5, total_read_time, total_read_pages
		FROM income_db.book WHERE (title, authors, md5) NOT IN (SELECT title, authors, md5 FROM book);
		CREATE TEMP TABLE book_id_map AS
			SELECT m.id as mid, i.id as iid FROM book m
			INNER JOIN income_db.book i ON (m.title, m.authors, m.md5) = (i.title, i.authors, i.md5);`
	if cacheErr == nil {
		q += `
		DELETE FROM income_db.page_stat_data WHERE (id_book, page, start_time) IN (
			SELECT map.iid, page, start_time FROM cached_db.page_stat_data
			INNER JOIN book_id_map AS map ON id_book = map.mid
			WHERE (id_book, page, start_time) NOT IN (SELECT id_book, page, start_time FROM page_stat_data));
		DELETE FROM page_stat_data WHERE (id_book, page, start_time) IN (
			SELECT id_book, page, start_time FROM cached_db.page_stat_data WHERE (id_book, page, start_time) NOT IN (
				SELECT map.mid, page, start_time FROM income_db.page_stat_data
				LEFT JOIN book_id_map AS map on id_book = map.iid));`
	}
	q += `
		INSERT INTO page_stat_data (id_book, page, start_time, duration, total_pages)
			SELECT map.mid, page, start_time, duration, total_pages
			FROM income_db.page_stat_data INNER JOIN book_id_map as map ON id_book = map.iid
			WHERE map.mid IS NOT null
		ON CONFLICT(id_book, page, start_time) DO UPDATE SET duration = MAX(duration, excluded.duration);
		UPDATE book SET (total_read_pages, total_read_time) =
			(SELECT count(DISTINCT page), sum(duration) FROM page_stat WHERE id_book = book.id);`

	db, err := sql.Open("sqlite", "file:"+local)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("onSync: %v", err)
	}
}

func readerBooks(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT title, (SELECT count(*) FROM page_stat_data WHERE id_book = book.id) FROM book ORDER BY title`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var title string
		var n int
		if err := rows.Scan(&title, &n); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s:%d", title, n))
	}
	return strings.Join(out, " ")
}

// TestWebDAVE2E: две читалки одного пользователя синхронизируют статистику
// впервые одновременно через настоящий serve. Вторая получает 412 на PUT без
// If-Match, повторяет цикл со слиянием, и итоговый снимок содержит данные обеих.
func TestWebDAVE2E(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("KSB_DATA_DIR", dataDir)
	var out, errb bytes.Buffer
	if code := run([]string{"user", "add", "alice"}, &out, &errb); code != 0 {
		t.Fatalf("user add: %s", errb.String())
	}
	passwords := map[string]string{}
	for _, d := range []string{"kobo", "pocketbook"} {
		out.Reset()
		if code := run([]string{"device", "add", "alice", d}, &out, &errb); code != 0 {
			t.Fatalf("device add: %s", errb.String())
		}
		m := regexp.MustCompile(`(?m)^  password: (\S+)$`).FindStringSubmatch(out.String())
		if m == nil {
			t.Fatalf("no password: %s", out.String())
		}
		passwords[d] = m[1]
	}
	base, _ := startServer(t)

	rec := func(page, start int64) statstest.Record {
		return statstest.Record{Page: page, StartTime: start, Duration: 40, TotalPages: 50}
	}
	reader := func(device string, books []statstest.Book) cloudReader {
		path := filepath.Join(t.TempDir(), statistics.FileStatistics)
		statstest.Write(t, path, books)
		return cloudReader{t: t, base: base, user: "alice", password: passwords[device], local: path}
	}
	kobo := reader("kobo", []statstest.Book{
		{ID: 1, Title: "Kobo book", Authors: "N/A", MD5: "11111111111111111111111111111111", Pages: 50,
			Records: []statstest.Record{rec(1, 100), rec(2, 200)}},
		{ID: 2, Title: "Shared", Authors: "A", MD5: "33333333333333333333333333333333", Pages: 50,
			Records: []statstest.Record{rec(1, 500)}},
	})
	pocketbook := reader("pocketbook", []statstest.Book{
		{ID: 1, Title: "Shared", Authors: "A", MD5: "33333333333333333333333333333333", Pages: 50,
			Records: []statstest.Record{rec(2, 600)}},
		{ID: 2, Title: "PocketBook book", Authors: "B", MD5: "22222222222222222222222222222222", Pages: 50,
			Records: []statstest.Record{rec(5, 900)}},
	})

	// Обе читалки получили 404; kobo выгрузила первой, PocketBook — после неё.
	puts := pocketbook.sync(func() {
		if n := kobo.sync(nil); n != 1 {
			t.Fatalf("kobo PUTs = %d", n)
		}
	})
	if puts != 2 {
		t.Fatalf("pocketbook PUTs = %d, want 2 (412, then merge)", puts)
	}
	if n := kobo.sync(nil); n != 1 {
		t.Fatalf("kobo second sync PUTs = %d", n)
	}
	want := "Kobo book:2 PocketBook book:1 Shared:2"
	for name, r := range map[string]cloudReader{"kobo": kobo, "pocketbook": pocketbook} {
		if got := readerBooks(t, r.local); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}

	res := kobo.request("GET", nil, "")
	snapshot, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.Header.Get("ETag") == "" {
		t.Fatal("GET from real serve has no ETag")
	}
	snap, err := statistics.Parse(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(dataDir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var books, records int
	if err := db.Read.QueryRow(`SELECT (SELECT count(*) FROM stat_books), (SELECT count(*) FROM stat_pages)`).Scan(&books, &records); err != nil {
		t.Fatal(err)
	}
	if books != len(snap.Books) || records != snap.Records() || books != 3 || records != 5 {
		t.Fatalf("server statistics %d books, %d records; snapshot %d, %d", books, records, len(snap.Books), snap.Records())
	}
}
