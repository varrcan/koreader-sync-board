package statistics

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics/statstest"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

var ctx = context.Background()

type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	t    *testing.T
	db   *store.DB
	acc  *accounts.Service
	s    *Store
	logs *logBuffer
}

func newEnv(t *testing.T, opts ...Option) *env {
	t.Helper()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logs := &logBuffer{}
	return &env{t: t, db: db, acc: accounts.New(db, accounts.WithArgon2Params(accounts.TestArgon2Params)),
		s: New(db, t.TempDir(), slog.New(slog.NewJSONHandler(logs, nil)), opts...), logs: logs}
}

func (e *env) user(name string) int64 {
	e.t.Helper()
	u, err := e.acc.CreateUser(ctx, name, accounts.RoleUser)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

func (e *env) device(user, name string) accounts.Principal {
	e.t.Helper()
	_, pass, err := e.acc.AddDevice(ctx, user, name)
	if err != nil {
		e.t.Fatal(err)
	}
	p, err := e.acc.Authenticate(ctx, user, accounts.KeyFromPassword(pass), netip.Addr{})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// upload — выгрузка с устройства без условия по ETag текущего файла.
func (e *env) upload(p accounts.Principal, content []byte) File {
	e.t.Helper()
	cond := Cond{}
	if cur, err := e.s.Get(ctx, p.UserID, FileStatistics); err == nil {
		cond.Match = []string{cur.ETag}
	}
	f, _, err := e.s.Save(ctx, p.UserID, &p, FileStatistics, content, cond)
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

// titles — книги статистики пользователя: «название|авторы|серия|язык|хеш:записей».
func (e *env) titles(userID int64) string {
	e.t.Helper()
	rows, err := e.db.Read.QueryContext(ctx, `
		SELECT coalesce(b.title, '-'), coalesce(b.authors, '-'), coalesce(b.series, '-'), coalesce(b.language, '-'),
		       coalesce(d.hash, '-'), (SELECT count(*) FROM stat_pages p WHERE p.book_id = b.id AND p.user_id = b.user_id)
		FROM stat_books b LEFT JOIN documents d ON d.id = b.document_id
		WHERE b.user_id = ? ORDER BY b.title`, userID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var title, authors, series, lang, hash string
		var n int
		if err := rows.Scan(&title, &authors, &series, &lang, &hash, &n); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s|%s|%s|%s|%s:%d", title, authors, series, lang, hash, n))
	}
	return strings.Join(out, " ")
}

func records(n int, start int64) []statstest.Record {
	out := make([]statstest.Record, n)
	for i := range out {
		out[i] = statstest.Record{Page: int64(i + 1), StartTime: start + int64(i)*60, Duration: 30, TotalPages: 100}
	}
	return out
}

func rec(page, start, duration int64) statstest.Record {
	return statstest.Record{Page: page, StartTime: start, Duration: duration, TotalPages: 100}
}

func book(id int64, title, md5 string, recs []statstest.Record) statstest.Book {
	return statstest.Book{ID: id, Title: title, Authors: "Author " + title, MD5: md5, Series: "N/A", Language: "ru",
		Pages: 100, LastOpen: 1_700_000_000 + id, Records: recs}
}

func TestBuilderMatchesKOReader(t *testing.T) {
	data := statstest.Build(t, []statstest.Book{book(1, "A", "aaa", records(2, 1000))})
	if !bytes.HasPrefix(data, sqliteMagic) {
		t.Fatal("not an SQLite file")
	}
	if data[18] != 2 || data[19] != 2 {
		t.Fatalf("header bytes 18–19 = %d %d, want WAL (2 2)", data[18], data[19])
	}
	db, err := sql.Open("sqlite", "file:"+statstest.Path(t, nil)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 20221111 {
		t.Fatalf("user_version = %d, %v", v, err)
	}
}

func TestParse(t *testing.T) {
	data := statstest.Build(t, []statstest.Book{
		{ID: 1, Title: "Placeholders", Authors: "N/A", Series: "N/A", Language: "N/A", MD5: "aaa", Pages: 10,
			Records: records(3, 1000)},
		{ID: 2, Title: "Empty", Authors: "", Series: "", Language: "", Pages: 10, Records: records(1, 5000)},
		{ID: 3, Title: "Full", Authors: "X\nY", Series: "S", Language: "en", MD5: "ccc", Pages: 10},
	}, `INSERT INTO page_stat_data (id_book, page, start_time, duration, total_pages) VALUES (99, 1, 1, 1, 1), (NULL, 2, 2, 2, 2)`)

	snap, err := Parse(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Books) != 3 || snap.Records() != 4 || snap.Orphans != 2 {
		t.Fatalf("books %d, records %d, orphans %d", len(snap.Books), snap.Records(), snap.Orphans)
	}
	p := snap.Books[0]
	if p.Authors.Valid || p.Series.Valid || p.Language.Valid || p.MD5.String != "aaa" {
		t.Errorf("placeholders not normalized: %+v", p)
	}
	if e := snap.Books[1]; e.Authors.Valid || e.Series.Valid || e.Language.Valid || e.MD5.Valid {
		t.Errorf("empty strings not normalized: %+v", e)
	}
	if f := snap.Books[2]; f.Authors.String != "X\nY" || f.Series.String != "S" || f.Language.String != "en" {
		t.Errorf("full book: %+v", f)
	}
	if !bytes.Equal(data[18:20], []byte{2, 2}) {
		t.Error("Parse modified the caller's buffer")
	}
}

func TestParseRejects(t *testing.T) {
	sqlite := func(schema string) []byte {
		path := filepath.Join(t.TempDir(), "x.sqlite3")
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
		db.Close()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	valid := statstest.Build(t, []statstest.Book{book(1, "A", "aaa", records(50, 1000))})
	corrupt := bytes.Clone(valid)
	// Страница 2 и дальше — b-tree таблиц; мусор в них ловит quick_check.
	pageSize := int(corrupt[16])<<8 | int(corrupt[17])
	for i := pageSize + 8; i < len(corrupt); i++ {
		corrupt[i] ^= 0x5a
	}

	tests := map[string][]byte{
		"not sqlite":         []byte("hello, this is not a database at all, definitely not"),
		"truncated header":   valid[:16],
		"no page_stat_data":  sqlite(`CREATE TABLE book (id integer PRIMARY KEY, title, authors, notes, last_open, highlights, pages, series, language, md5, total_read_time, total_read_pages)`),
		"missing column":     sqlite(statstest.Schema + `; ALTER TABLE book DROP COLUMN highlights`),
		"quick_check failed": corrupt,
	}
	for name, data := range tests {
		if _, err := Parse(ctx, data); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestParseExtraColumns(t *testing.T) {
	data := statstest.Build(t, []statstest.Book{book(1, "A", "aaa", records(2, 1000))},
		`ALTER TABLE book ADD COLUMN future text`, `ALTER TABLE page_stat_data ADD COLUMN future integer`)
	snap, err := Parse(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Books) != 1 || snap.Records() != 2 {
		t.Fatalf("books %d, records %d", len(snap.Books), snap.Records())
	}
}

func TestSaveReplacesStatistics(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	p := e.device("alice", "Kobo")

	e.upload(p, statstest.Build(t, []statstest.Book{
		book(1, "A", "aaa", records(3, 1000)),
		book(2, "B", "", records(2, 5000)),
	}, `INSERT INTO page_stat_data (id_book, page, start_time, duration, total_pages) VALUES (99, 1, 1, 1, 1)`))
	if got, want := e.titles(alice), "A|Author A|-|ru|aaa:3 B|Author B|-|ru|-:2"; got != want {
		t.Fatalf("after first upload: %s, want %s", got, want)
	}
	if !strings.Contains(e.logs.String(), `"msg":"statistics: page records without book skipped","user_id":1,"count":1`) {
		t.Errorf("orphans not logged:\n%s", e.logs)
	}

	// Статистику книги A сбросили на читалке.
	e.upload(p, statstest.Build(t, []statstest.Book{book(2, "B", "", records(4, 5000))}))
	if got, want := e.titles(alice), "B|Author B|-|ru|-:4"; got != want {
		t.Fatalf("after reset: %s, want %s", got, want)
	}
	var docs int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM documents WHERE hash = 'aaa'`).Scan(&docs); err != nil || docs != 1 {
		t.Fatalf("documents registry: %d, %v", docs, err)
	}
	var orphanPages int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM stat_pages WHERE book_id NOT IN (SELECT id FROM stat_books)`).Scan(&orphanPages); err != nil || orphanPages != 0 {
		t.Fatalf("stale pages: %d, %v", orphanPages, err)
	}
}

func TestSaveTwoUsers(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	e.upload(e.device("alice", "Kobo"), statstest.Build(t, []statstest.Book{book(1, "Shared", "same", records(2, 1000))}))
	e.upload(e.device("bob", "Kindle"), statstest.Build(t, []statstest.Book{
		book(1, "Shared", "same", records(5, 1000)), book(2, "Own", "bbb", nil)}))
	if got := e.titles(alice); got != "Shared|Author Shared|-|ru|same:2" {
		t.Errorf("alice: %s", got)
	}
	if got := e.titles(bob); got != "Own|Author Own|-|ru|bbb:0 Shared|Author Shared|-|ru|same:5" {
		t.Errorf("bob: %s", got)
	}
	var n int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM documents WHERE hash = 'same'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("documents: %d, %v", n, err)
	}
	if _, err := e.s.Get(ctx, bob, FileVocabulary); !errors.Is(err, ErrNotFound) {
		t.Fatalf("vocabulary: %v", err)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	p := e.device("alice", "Kobo")
	first := e.upload(p, statstest.Build(t, []statstest.Book{book(1, "A", "aaa", records(2, 1000))}))
	if _, err := e.db.Write.Exec(`CREATE TRIGGER fail BEFORE INSERT ON stat_pages WHEN NEW.page = 77
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.s.Save(ctx, alice, &p, FileStatistics,
		statstest.Build(t, []statstest.Book{book(1, "B", "bbb", records(80, 1000))}), Cond{Match: []string{first.ETag}})
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("err = %v", err)
	}
	cur, err := e.s.Get(ctx, alice, FileStatistics)
	if err != nil || cur.ETag != first.ETag {
		t.Fatalf("file changed: %v %v", cur.ETag, err)
	}
	if got := e.titles(alice); got != "A|Author A|-|ru|aaa:2" {
		t.Fatalf("statistics changed: %s", got)
	}
}

func TestSaveConditions(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	p := e.device("alice", "Kobo")
	v1 := []byte("SQLite format 3\x00 vocabulary 1")
	v2 := []byte("SQLite format 3\x00 vocabulary 2")

	f, created, err := e.s.Save(ctx, alice, &p, FileVocabulary, v1, Cond{})
	if err != nil || !created || f.ETag != ETag(v1) || len(f.ETag) != 34 {
		t.Fatalf("create: %+v %v %v", f, created, err)
	}
	for name, c := range map[string]Cond{
		"no If-Match on existing": {},
		"wrong etag":              {Match: []string{`"0000"`}},
		"weak etag":               {Match: []string{"W/" + f.ETag}},
	} {
		if _, _, err := e.s.Save(ctx, alice, &p, FileVocabulary, v2, c); !errors.Is(err, ErrPrecondition) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := e.s.Save(ctx, alice, &p, FileVocabulary, v2, Cond{Match: []string{`"x"`, f.ETag}}); err != nil {
		t.Fatal(err)
	}
	stats := statstest.Build(t, nil)
	if _, _, err := e.s.Save(ctx, alice, &p, FileStatistics, stats, Cond{Match: []string{"*"}}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("* on missing file: %v", err)
	}
	if _, _, err := e.s.Save(ctx, alice, &p, FileVocabulary, []byte("plain text"), Cond{Match: []string{"*"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not sqlite: %v", err)
	}
	if _, _, err := e.s.Save(ctx, alice, &p, "books.epub", v1, Cond{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("name: %v", err)
	}
	got, err := e.s.Get(ctx, alice, FileVocabulary)
	if err != nil || !bytes.Equal(got.Content, v2) || got.ETag != ETag(v2) {
		t.Fatalf("get: %q %v", got.Content, err)
	}

	if err := e.acc.RevokeDevice(ctx, "alice", "Kobo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.Save(ctx, alice, &p, FileVocabulary, v1, Cond{Match: []string{"*"}}); !errors.Is(err, accounts.ErrUnauthorized) {
		t.Fatalf("revoked: %v", err)
	}
}

// readSnapshot открывает снимок и возвращает книги («название:записей:итог времени»)
// и длительность записи (книга, страница, start_time).
func readSnapshot(t *testing.T, data []byte) (string, map[string]int64) {
	t.Helper()
	m, err := openMemory(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.quickCheck(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := m.conn.QueryContext(ctx, `
		SELECT b.title, b.last_open, coalesce(b.total_read_time, 0), (SELECT count(*) FROM page_stat_data WHERE id_book = b.id)
		FROM book b ORDER BY b.title`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var books []string
	for rows.Next() {
		var title string
		var lastOpen, total, n int64
		if err := rows.Scan(&title, &lastOpen, &total, &n); err != nil {
			t.Fatal(err)
		}
		books = append(books, fmt.Sprintf("%s:%d:%d:%d", title, n, total, lastOpen))
	}
	rows.Close()
	durations := map[string]int64{}
	rows, err = m.conn.QueryContext(ctx, `
		SELECT b.title, p.page, p.start_time, p.duration FROM page_stat_data p JOIN book b ON b.id = p.id_book`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		var page, start, d int64
		if err := rows.Scan(&title, &page, &start, &d); err != nil {
			t.Fatal(err)
		}
		durations[fmt.Sprintf("%s/%d/%d", title, page, start)] = d
	}
	return strings.Join(books, " "), durations
}

func TestImportWithoutSnapshot(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	data := statstest.Build(t, []statstest.Book{book(1, "A", "aaa", records(2, 1000))})
	f, err := e.s.Import(ctx, alice, data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.s.Get(ctx, alice, FileStatistics)
	if err != nil || !bytes.Equal(got.Content, data) || got.ETag != f.ETag {
		t.Fatalf("snapshot is not the uploaded file: %v", err)
	}
	st, err := e.s.Status(ctx, alice)
	if err != nil || st.Source != SourceImport || st.DeviceName != nil || st.Books != 1 || st.Records != 2 {
		t.Fatalf("status: %+v %v", st, err)
	}
}

func TestImportMerges(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	p := e.device("alice", "Kobo")
	// Снимок с читалки: книги A и B; запись A/1/1000 длится 30, A/2/1060 — 50.
	a := book(1, "A", "aaa", []statstest.Record{rec(1, 1000, 30), rec(2, 1060, 50)})
	a.LastOpen = 2000
	before := e.upload(p, statstest.Build(t, []statstest.Book{a, book(2, "B", "bbb", records(3, 7000))}))

	// Файл со старой читалки: A под другим id, A/1/1000 длится 45, A/2/1060 — 20,
	// новая запись A/3; книга C, которой в снимке нет.
	a2 := book(5, "A", "aaa", []statstest.Record{rec(1, 1000, 45), rec(2, 1060, 20), rec(3, 1200, 10)})
	a2.LastOpen = 3000
	f, err := e.s.Import(ctx, alice, statstest.Build(t, []statstest.Book{a2, book(6, "C", "ccc", records(2, 9000))}))
	if err != nil {
		t.Fatal(err)
	}
	if f.ETag == before.ETag {
		t.Fatal("ETag did not change")
	}
	cur, err := e.s.Get(ctx, alice, FileStatistics)
	if err != nil {
		t.Fatal(err)
	}
	books, durations := readSnapshot(t, cur.Content)
	// A: 3 записи, итог 45+50+10; last_open — больший; B и C на месте.
	if want := "A:3:105:3000 B:3:90:1700000002 C:2:60:1700000006"; books != want {
		t.Errorf("books %s, want %s", books, want)
	}
	if durations["A/1/1000"] != 45 || durations["A/2/1060"] != 50 || durations["A/3/1200"] != 10 {
		t.Errorf("durations %v", durations)
	}
	if got := e.titles(alice); got != "A|Author A|-|ru|aaa:3 B|Author B|-|ru|bbb:3 C|Author C|-|ru|ccc:2" {
		t.Errorf("statistics: %s", got)
	}
	st, err := e.s.Status(ctx, alice)
	if err != nil || st.Source != SourceImport || st.Books != 3 || st.Records != 8 {
		t.Fatalf("status %+v %v", st, err)
	}
}

func TestImportRetriesOnConcurrentSync(t *testing.T) {
	var e *env
	var p accounts.Principal
	calls := 0
	e = newEnv(t, WithBeforeImportSave(func() {
		calls++
		if calls == 1 {
			// Читалка выгрузила новый снимок между объединением и записью.
			e.upload(p, statstest.Build(t, []statstest.Book{book(1, "A", "aaa", nil), book(2, "D", "ddd", records(1, 100))}))
		}
	}))
	alice := e.user("alice")
	p = e.device("alice", "Kobo")
	e.upload(p, statstest.Build(t, []statstest.Book{book(1, "A", "aaa", nil)}))

	if _, err := e.s.Import(ctx, alice, statstest.Build(t, []statstest.Book{book(1, "C", "ccc", nil)})); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("merge attempts = %d, want 2", calls)
	}
	cur, _ := e.s.Get(ctx, alice, FileStatistics)
	if books, _ := readSnapshot(t, cur.Content); !strings.Contains(books, "C:") || !strings.Contains(books, "D:") {
		t.Fatalf("lost data: %s", books)
	}
}

func TestImportConflict(t *testing.T) {
	var e *env
	var p accounts.Principal
	n := 0
	e = newEnv(t, WithBeforeImportSave(func() {
		n++
		e.upload(p, statstest.Build(t, []statstest.Book{book(1, fmt.Sprint("X", n), "x", nil)}))
	}))
	alice := e.user("alice")
	p = e.device("alice", "Kobo")
	e.upload(p, statstest.Build(t, []statstest.Book{book(1, "A", "aaa", nil)}))
	if _, err := e.s.Import(ctx, alice, statstest.Build(t, []statstest.Book{book(1, "C", "ccc", nil)})); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if n != importAttempts {
		t.Fatalf("attempts = %d", n)
	}
}

func TestImportRejectsInvalid(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	if _, err := e.s.Import(ctx, alice, []byte("SQLite format 3\x00 garbage")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if st, err := e.s.Status(ctx, alice); st != nil || err != nil {
		t.Fatalf("status %+v %v", st, err)
	}
	if !strings.Contains(e.logs.String(), `"msg":"statistics: file rejected"`) {
		t.Errorf("rejection not logged:\n%s", e.logs)
	}
}

func TestStatus(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	e := newEnv(t, WithClock(func() time.Time { return now }))
	alice := e.user("alice")
	if st, err := e.s.Status(ctx, alice); st != nil || err != nil {
		t.Fatalf("empty: %+v %v", st, err)
	}
	p := e.device("alice", "pocketbook")
	data := statstest.Build(t, []statstest.Book{book(1, "A", "aaa", records(3, 1000)), book(2, "B", "bbb", records(1, 500))})
	e.upload(p, data)
	st, err := e.s.Status(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if st.DeviceName == nil || *st.DeviceName != "pocketbook" || st.Source != SourceWebDAV || st.Size != int64(len(data)) ||
		st.Books != 2 || st.Records != 4 || !st.UploadedAt.Equal(now) ||
		st.FirstReadAt.Unix() != 500 || st.LastReadAt.Unix() != 1120 {
		t.Fatalf("status %+v", st)
	}
}

// TestOracle разбирает настоящий statistics.sqlite3 с PocketBook. Путь — в
// KSB_ORACLE_STATS; без переменной тест пропускается.
func TestOracle(t *testing.T) {
	path := os.Getenv("KSB_ORACLE_STATS")
	if path == "" {
		t.Skip("KSB_ORACLE_STATS not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	alice := e.user("alice")
	e.upload(e.device("alice", "pocketbook"), data)
	st, err := e.s.Status(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if st.Books != 33 || st.Records != 25969 {
		t.Fatalf("books %d, records %d, want 33 and 25969", st.Books, st.Records)
	}
	var missing int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM stat_books WHERE document_id IS NULL`).Scan(&missing); err != nil || missing != 0 {
		t.Fatalf("books without document: %d, %v", missing, err)
	}
	// Импорт того же файла поверх снимка ничего не теряет и не дублирует.
	if _, err := e.s.Import(ctx, alice, data); err != nil {
		t.Fatal(err)
	}
	if st, err = e.s.Status(ctx, alice); err != nil || st.Books != 33 || st.Records != 25969 {
		t.Fatalf("after import: %+v %v", st, err)
	}
}
