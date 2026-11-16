package statistics

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/statistics/statstest"
)

func unix(y int, m time.Month, d, h, min int) int64 {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC).Unix()
}

func ymd(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// now — «сегодня» тестов: среда 4 ноября 2026, полдень UTC.
var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// snapshot сохраняет снимок пользователя name, принятый в asOf; extra — SQL поверх
// базы KOReader.
func (e *env) snapshot(name string, asOf time.Time, books []statstest.Book, extra ...string) int64 {
	e.t.Helper()
	var id int64
	err := e.db.Read.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = e.user(name)
	} else if err != nil {
		e.t.Fatal(err)
	}
	s := New(e.db, e.t.TempDir(), e.s.log, WithClock(func() time.Time { return asOf }))
	cond := Cond{}
	if cur, err := e.s.Get(ctx, id, FileStatistics); err == nil {
		cond.Match = []string{cur.ETag}
	}
	if _, _, err := s.Save(ctx, id, nil, FileStatistics, statstest.Build(e.t, books, extra...), cond); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// withProgress добавляет пользователю прогресс по хешу с названием из metadata.
func (e *env) withProgress(userID int64, hash, title string) {
	e.t.Helper()
	res, err := e.db.Write.ExecContext(ctx, `
		INSERT INTO progress_events (user_id, document_id, percentage, progress, device, server_ts)
		SELECT ?, id, 0.5, '/body', 'dev', 1 FROM documents WHERE hash = ?`, userID, hash)
	if err != nil {
		e.t.Fatal(err)
	}
	ev, _ := res.LastInsertId()
	if _, err := e.db.Write.ExecContext(ctx, `
		INSERT INTO progress_current (user_id, document_id, event_id, meta_title)
		SELECT user_id, document_id, id, ? FROM progress_events WHERE id = ?`, title, ev); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) summary(userID int64, p Period, tz string, at time.Time) Summary {
	e.t.Helper()
	s, err := e.s.Summary(ctx, userID, p, mustLoc(e.t, tz), at)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) allSessions(userID int64) []Session {
	e.t.Helper()
	out, next, err := e.s.Sessions(ctx, userID, nil, nil, 1000)
	if err != nil || next != nil {
		e.t.Fatalf("sessions: next %v, %v", next, err)
	}
	return out
}

func bucketAt(t *testing.T, s Summary, d time.Time) Bucket {
	t.Helper()
	for _, b := range s.Series {
		if b.Date.Equal(d) {
			return b
		}
	}
	t.Fatalf("no bucket %s in %v", d.Format(time.DateOnly), s.Series)
	return Bucket{}
}

func checkSums(t *testing.T, s Summary) {
	t.Helper()
	var dur, hours int64
	var pages int
	for _, b := range s.Series {
		dur += b.Duration
		pages += b.Pages
	}
	for _, h := range s.Hours {
		hours += h
	}
	if dur != s.Totals.Duration || pages != s.Totals.Pages || hours != s.Totals.Duration {
		t.Fatalf("series %d s / %d pages, hours %d s, totals %+v", dur, pages, hours, s.Totals)
	}
}

func statBook(id int64, title, md5 string, pages int64, recs ...statstest.Record) statstest.Book {
	return statstest.Book{ID: id, Title: title, Authors: "Author", MD5: md5, Series: "N/A", Language: "ru",
		Pages: pages, LastOpen: 1_700_000_000 + id, Records: recs}
}

func pageRec(page, total, start, duration int64) statstest.Record {
	return statstest.Record{Page: page, StartTime: start, Duration: duration, TotalPages: total}
}

func TestPageRange(t *testing.T) {
	for _, c := range []struct{ page, total, pages, first, last int64 }{
		{5, 100, 200, 9, 10},
		{5, 100, 100, 5, 5},
		{5, 100, 50, 3, 3},
		{6, 100, 50, 3, 3},
		{1, 100, 50, 1, 1},
		{5, 100, 0, 1, 1},
		{1, 1, 5000, 1, 1000},
	} {
		first, last := pageRange(c.page, c.total, c.pages)
		if first != c.first || last != c.last {
			t.Errorf("pageRange(%d, %d, %d) = %d–%d, want %d–%d", c.page, c.total, c.pages, first, last, c.first, c.last)
		}
	}
}

func TestPagesLikePageStat(t *testing.T) {
	e := newEnv(t)
	day := unix(2026, 11, 4, 8, 0)
	alice := e.snapshot("alice", now, []statstest.Book{
		statBook(1, "Bigger", "aaa", 200, pageRec(5, 100, day, 60)),
		statBook(2, "Smaller", "bbb", 50, pageRec(5, 100, day+100, 60), pageRec(6, 100, day+200, 60)),
		statBook(3, "Same", "ccc", 100, pageRec(5, 100, day+300, 60), pageRec(5, 100, day+400, 60)),
		statBook(4, "Zero", "ddd", 0, pageRec(5, 100, day+500, 60), pageRec(6, 100, day+600, 60)),
		statBook(5, "Null", "eee", 100, pageRec(5, 100, day+700, 60)),
		statBook(6, "NoTotal", "fff", 100, pageRec(5, 0, day+800, 60)),
	}, `UPDATE book SET pages = NULL WHERE id = 5`)
	s := e.summary(alice, PeriodWeek, "UTC", now)
	checkSums(t, s)
	got := map[string][2]int64{}
	for _, b := range s.Books {
		got[*b.Title] = [2]int64{b.Duration, int64(b.Pages)}
	}
	want := map[string][2]int64{"Bigger": {60, 2}, "Smaller": {120, 1}, "Same": {120, 1}, "Zero": {120, 1}}
	if len(got) != len(want) {
		t.Fatalf("books %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}
	if s.Totals.Duration != 420 || s.Totals.Pages != 5 {
		t.Fatalf("totals %+v", s.Totals)
	}
}

func TestDaysAndHoursInTimeZone(t *testing.T) {
	e := newEnv(t)
	alice := e.snapshot("alice", now, []statstest.Book{
		statBook(1, "A", "aaa", 100, pageRec(1, 100, unix(2026, 11, 3, 21, 30), 60)),
	})
	msk := e.summary(alice, PeriodWeek, "Europe/Moscow", now)
	if b := bucketAt(t, msk, ymd(2026, 11, 4)); b.Duration != 60 || msk.Hours[0] != 60 {
		t.Fatalf("Moscow: %+v, hours %v", b, msk.Hours)
	}
	utc := e.summary(alice, PeriodWeek, "UTC", now)
	if b := bucketAt(t, utc, ymd(2026, 11, 3)); b.Duration != 60 || utc.Hours[21] != 60 {
		t.Fatalf("UTC: %+v, hours %v", b, utc.Hours)
	}
}

func TestDaylightSavingDay(t *testing.T) {
	e := newEnv(t)
	// 29 марта 2026 в Берлине — 23 часа: с 00:00 CET (28 марта 23:00 UTC) до 00:00 CEST
	// (29 марта 22:00 UTC). Запись каждый час UTC и одна — в полночь 30-го.
	var recs []statstest.Record
	start := unix(2026, 3, 28, 23, 0)
	for i := range int64(24) {
		recs = append(recs, pageRec(i+1, 100, start+i*3600, 60))
	}
	alice := e.snapshot("alice", time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC),
		[]statstest.Book{statBook(1, "A", "aaa", 100, recs...)})
	s := e.summary(alice, PeriodWeek, "Europe/Berlin", time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC))
	checkSums(t, s)
	if b := bucketAt(t, s, ymd(2026, 3, 29)); b.Duration != 23*60 || b.Pages != 23 {
		t.Fatalf("29 March: %+v", b)
	}
	if b := bucketAt(t, s, ymd(2026, 3, 30)); b.Duration != 60 {
		t.Fatalf("30 March: %+v", b)
	}
	if s.Totals.Duration != 24*60 || s.Hours[2] != 0 || s.Hours[0] != 120 {
		t.Fatalf("totals %+v, hours %v", s.Totals, s.Hours)
	}
}

func TestSessions(t *testing.T) {
	e := newEnv(t)
	t0 := unix(2026, 11, 2, 18, 0)
	alice := e.snapshot("alice", now, []statstest.Book{
		// A: пауза ровно 600 с — та же сессия, 601 с — новая.
		statBook(1, "A", "aaa", 100,
			pageRec(1, 100, t0, 100), pageRec(2, 100, t0+700, 100), pageRec(3, 100, t0+1401, 100)),
		// B через минуту после A — новая сессия; вторая читалка перекрывает первую:
		// пауза считается от наибольшего конца, а не от конца последней записи.
		statBook(2, "B", "bbb", 100,
			pageRec(1, 100, t0+1561, 1000), pageRec(2, 100, t0+1600, 10), pageRec(3, 100, t0+3000, 10)),
		// Две строки одного хеша подряд — одна книга и одна сессия.
		statBook(3, "C old", "ccc", 100, pageRec(1, 100, t0+5000, 60)),
		statBook(4, "C new", "ccc", 100, pageRec(2, 100, t0+5100, 60), pageRec(1, 100, t0+5200, 60)),
	})
	got := e.allSessions(alice)
	type span struct {
		title      string
		start, end int64
		dur        int64
		pages      int
	}
	var spans []span
	for _, s := range got {
		spans = append(spans, span{*s.Book.Title, s.StartedAt.Unix() - t0, s.EndedAt.Unix() - t0, s.Duration, s.Pages})
	}
	want := []span{
		{"C new", 5000, 5260, 180, 2},
		{"B", 1561, 3010, 1020, 3},
		{"A", 1401, 1501, 100, 1},
		{"A", 0, 800, 200, 2},
	}
	if !slices.Equal(spans, want) {
		t.Fatalf("sessions\n got %v\nwant %v", spans, want)
	}
}

func TestSessionAcrossMidnight(t *testing.T) {
	e := newEnv(t)
	alice := e.snapshot("alice", now, []statstest.Book{statBook(1, "A", "aaa", 100,
		pageRec(1, 100, unix(2026, 11, 3, 23, 50), 600), pageRec(2, 100, unix(2026, 11, 4, 0, 5), 600))})
	s := e.summary(alice, PeriodWeek, "UTC", now)
	if s.Totals.Sessions != 1 || bucketAt(t, s, ymd(2026, 11, 3)).Duration != 600 || bucketAt(t, s, ymd(2026, 11, 4)).Duration != 600 {
		t.Fatalf("totals %+v, series %v", s.Totals, s.Series)
	}
	// Неделя с 4 ноября: сессия начата 3-го — она в прошлом периоде, а время 4-го — в текущем.
	s = e.summary(alice, PeriodWeek, "UTC", time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	if s.Totals.Sessions != 0 || s.Totals.Duration != 600 || s.Previous.Sessions != 1 || s.Previous.Duration != 600 {
		t.Fatalf("totals %+v, previous %+v", s.Totals, s.Previous)
	}
}

func TestSummaryPeriods(t *testing.T) {
	e := newEnv(t)
	at := func(y int, m time.Month, d int) int64 { return unix(y, m, d, 10, 0) }
	alice := e.snapshot("alice", now, []statstest.Book{statBook(1, "A", "aaa", 100,
		pageRec(1, 100, at(2024, 11, 30), 1), // до прошлого года
		pageRec(1, 100, at(2024, 12, 1), 2),  // прошлый год
		pageRec(1, 100, at(2025, 9, 5), 4),
		pageRec(1, 100, at(2025, 9, 6), 8),
		pageRec(1, 100, at(2025, 10, 5), 16),
		pageRec(1, 100, at(2025, 10, 6), 32),
		pageRec(1, 100, at(2025, 11, 30), 64), // последний день прошлого года
		pageRec(1, 100, at(2025, 12, 1), 128), // первый день периода
	)})
	// Неделя и месяц — на записях 2026-го.
	bob := e.snapshot("bob", now, []statstest.Book{statBook(1, "A", "aaa", 100,
		pageRec(1, 100, at(2026, 9, 5), 1),
		pageRec(1, 100, at(2026, 9, 6), 2),
		pageRec(1, 100, at(2026, 10, 5), 4),
		pageRec(1, 100, at(2026, 10, 6), 8),
		pageRec(1, 100, at(2026, 10, 21), 16),
		pageRec(1, 100, at(2026, 10, 22), 32),
		pageRec(1, 100, at(2026, 10, 28), 64),
		pageRec(1, 100, at(2026, 10, 29), 128),
		pageRec(1, 100, at(2026, 11, 4), 256),
		pageRec(1, 100, at(2026, 11, 5), 512), // часы читалки впереди
	)})

	w := e.summary(bob, PeriodWeek, "UTC", now)
	if !w.From.Equal(ymd(2026, 10, 29)) || !w.To.Equal(ymd(2026, 11, 4)) || w.Monthly || len(w.Series) != 7 {
		t.Fatalf("week %s–%s, %d buckets", w.From, w.To, len(w.Series))
	}
	if w.Totals.Duration != 128+256 || w.Previous.Duration != 32+64 || w.Totals.Days != 2 || w.Previous.Days != 2 {
		t.Fatalf("week totals %+v, previous %+v", w.Totals, w.Previous)
	}
	checkSums(t, w)

	m := e.summary(bob, PeriodMonth, "UTC", now)
	if !m.From.Equal(ymd(2026, 10, 6)) || len(m.Series) != 30 {
		t.Fatalf("month from %s, %d buckets", m.From, len(m.Series))
	}
	if m.Totals.Duration != 8+16+32+64+128+256 || m.Previous.Duration != 2+4 {
		t.Fatalf("month totals %+v, previous %+v", m.Totals, m.Previous)
	}
	checkSums(t, m)

	y := e.summary(alice, PeriodYear, "UTC", now)
	if !y.From.Equal(ymd(2025, 12, 1)) || !y.Monthly || len(y.Series) != 12 ||
		!y.Series[0].Date.Equal(ymd(2025, 12, 1)) || !y.Series[11].Date.Equal(ymd(2026, 11, 1)) {
		t.Fatalf("year from %s, %d buckets: %v", y.From, len(y.Series), y.Series)
	}
	if y.Totals.Duration != 128 || y.Previous.Duration != 2+4+8+16+32+64 || y.Series[0].Duration != 128 {
		t.Fatalf("year totals %+v, previous %+v", y.Totals, y.Previous)
	}
	checkSums(t, y)
}

func TestSummaryPagesAndBooks(t *testing.T) {
	e := newEnv(t)
	d3, d4 := unix(2026, 11, 3, 10, 0), unix(2026, 11, 4, 10, 0)
	books := []statstest.Book{
		// Страница 10 дважды за день — 1 страница; в другой день — ещё одна.
		statBook(1, "A", "aaa", 100, pageRec(10, 100, d3, 60), pageRec(10, 100, d3+100, 60), pageRec(10, 100, d4, 60)),
		// Две строки хеша: название — у строки с поздним last_open и непустым title.
		statBook(2, "H old", "hhh", 100, pageRec(1, 100, d3+1000, 3600)),
		statBook(3, "H new", "hhh", 100, pageRec(2, 100, d3+5000, 3600)),
		statBook(4, "", "hhh", 100),
		// Без хеша.
		statBook(5, "Loose", "", 100, pageRec(1, 100, d4+100, 30)),
		// Название из прогресса.
		statBook(6, "Stat title", "ppp", 100, pageRec(1, 100, d4+200, 7200)),
	}
	for i := range int64(8) {
		books = append(books, statBook(10+i, "Filler", "f"+string(rune('a'+i)), 100, pageRec(1, 100, d4+1000+i, 1)))
	}
	alice := e.snapshot("alice", now, books, `UPDATE book SET last_open = 1800000000 WHERE id IN (3, 4)`)
	e.withProgress(alice, "ppp", "Progress title")
	e.snapshot("bob", now, []statstest.Book{statBook(1, "Bob", "bbb", 100, pageRec(1, 100, d4, 99999))})

	s := e.summary(alice, PeriodWeek, "UTC", now)
	checkSums(t, s)
	if b := bucketAt(t, s, ymd(2026, 11, 3)); b.Pages != 3 || b.Duration != 120+7200 {
		t.Fatalf("3 Nov: %+v", b)
	}
	if s.Totals.Days != 2 || s.Totals.Pages != 3+1+1+1+8 {
		t.Fatalf("totals %+v", s.Totals)
	}
	if len(s.Books) != topBooks {
		t.Fatalf("%d books", len(s.Books))
	}
	top := s.Books[:4]
	if *top[0].Title != "H new" || top[0].Duration != 7200 || top[0].Pages != 2 || *top[0].Hash != "hhh" || top[0].HasProgress {
		t.Errorf("first %+v", top[0])
	}
	if *top[1].Title != "Progress title" || !top[1].HasProgress || top[1].Duration != 7200 {
		t.Errorf("second %+v", top[1])
	}
	if *top[2].Title != "A" || top[2].Duration != 180 || top[2].Pages != 2 {
		t.Errorf("third %+v", top[2])
	}
	if *top[3].Title != "Loose" || top[3].Hash != nil {
		t.Errorf("fourth %+v", top[3])
	}
	for _, b := range s.Books {
		if *b.Title == "Bob" {
			t.Fatal("other user's book visible")
		}
	}
}

// exec выполняет запрос на запись и возвращает id вставленной строки.
func (e *env) exec(q string, args ...any) int64 {
	e.t.Helper()
	res, err := e.db.Write.ExecContext(ctx, q, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// libFile добавляет в книгу библиотеки book (0 — новая с названием title) файл с
// хешем hash, как после скана. Возвращает id книги.
func (e *env) libFile(book int64, hash, title string) int64 {
	e.t.Helper()
	e.exec(`INSERT INTO library_roots (id, name, path, created_at) VALUES (1, 'books', '/books', 0) ON CONFLICT DO NOTHING`)
	if book == 0 {
		book = e.exec(`INSERT INTO library_books (title, created_at) VALUES (?, 0)`, title)
	}
	file := e.exec(`INSERT INTO library_files (root_id, path, size, mtime_ns, format, partial_md5, book_id, indexed_at)
		VALUES (1, ?, 1, 1, 'epub', ?, ?, 0)`, hash+".epub", hash, book)
	e.exec(`INSERT INTO library_file_hashes (hash, file_id) VALUES (?, ?)`, hash, file)
	return book
}

// Названия из библиотеки — как в списке книг: правка важнее прогресса и статистики,
// метаданные файла — только если других нет.
func TestSummaryTitlesFromLibrary(t *testing.T) {
	e := newEnv(t)
	d4 := unix(2026, 11, 4, 10, 0)
	alice := e.snapshot("alice", now, []statstest.Book{
		statBook(1, "(КВ - 10) Перекрестки сумерек", "edited", 100, pageRec(1, 100, d4, 300)),
		statBook(2, "Stat title", "plain", 100, pageRec(1, 100, d4+1000, 200)),
		statBook(3, "", "untitled", 100, pageRec(1, 100, d4+2000, 100)),
	})
	e.withProgress(alice, "edited", "Progress title")
	edited := e.libFile(0, "edited", "File title")
	e.exec(`INSERT INTO library_book_meta (book_id, title, authors, updated_at) VALUES (?, 'Перекрёстки Сумерек', 'Роберт Джордан', 0)`, edited)
	e.libFile(0, "plain", "File title")
	e.libFile(0, "untitled", "Lib title")

	got := map[string]BookTotal{}
	for _, b := range e.summary(alice, PeriodWeek, "UTC", now).Books {
		got[*b.Hash] = b
	}
	if b := got["edited"]; *b.Title != "Перекрёстки Сумерек" || *b.Authors != "Роберт Джордан" {
		t.Errorf("edited: %s / %v", *b.Title, b.Authors)
	}
	if b := got["plain"]; *b.Title != "Stat title" {
		t.Errorf("plain: %s", *b.Title)
	}
	if b := got["untitled"]; *b.Title != "Lib title" {
		t.Errorf("untitled: %s", *b.Title)
	}
}

// FB2 и EPUB одной книги библиотеки — разные хеши для KOReader, но одна книга в
// статистике; карточка — от хеша с прогрессом.
func TestLibraryBookFilesMerged(t *testing.T) {
	e := newEnv(t)
	d3, d4 := unix(2026, 11, 3, 10, 0), unix(2026, 11, 4, 10, 0)
	alice := e.snapshot("alice", now, []statstest.Book{
		statBook(1, "Перекрестки сумерек", "fb2", 1211, pageRec(1, 1211, d3, 600)),
		statBook(2, "(КВ - 10) Перекрестки сумерек", "epub", 1232, pageRec(2, 1232, d4, 300)),
		statBook(3, "Other", "other", 100, pageRec(1, 100, d4+1000, 60)),
	})
	e.withProgress(alice, "epub", "Progress title")
	book := e.libFile(0, "fb2", "Перекрёстки Сумерек")
	e.libFile(book, "epub", "")
	e.libFile(0, "other", "Other")

	s := e.summary(alice, PeriodWeek, "UTC", now)
	if len(s.Books) != 2 {
		t.Fatalf("books %+v", s.Books)
	}
	b := s.Books[0]
	if *b.Hash != "epub" || !b.HasProgress || *b.Title != "Progress title" || b.Duration != 900 || b.Pages != 2 {
		t.Errorf("merged %+v", b)
	}
	for _, hash := range []string{"fb2", "epub"} {
		_, st, err := e.s.Book(ctx, alice, hash, 0.5, time.UTC)
		if err != nil || st == nil || st.Duration != 900 || st.Sessions != 2 || *st.Pages != 1232 {
			t.Errorf("%s: %+v %v", hash, st, err)
		}
		ses, _, err := e.s.Sessions(ctx, alice, &hash, nil, 10)
		if err != nil || len(ses) != 2 {
			t.Errorf("%s sessions: %+v %v", hash, ses, err)
		}
	}
}

func TestSummaryWithoutSnapshot(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	e.snapshot("bob", now, []statstest.Book{statBook(1, "Bob", "bbb", 100, pageRec(1, 100, unix(2026, 11, 4, 1, 0), 60))})
	s := e.summary(alice, PeriodWeek, "UTC", now)
	if s.AsOf != nil || s.Totals != (Totals{}) || s.Previous != (Totals{}) || len(s.Books) != 0 || len(s.Series) != 7 {
		t.Fatalf("summary %+v", s)
	}
	for _, b := range s.Series {
		if b.NoData || b.Duration != 0 {
			t.Fatalf("bucket %+v", b)
		}
	}
}

func TestNoData(t *testing.T) {
	e := newEnv(t)
	recs := []statstest.Record{pageRec(1, 100, unix(2026, 11, 1, 10, 0), 60)}
	alice := e.snapshot("alice", time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), []statstest.Book{statBook(1, "A", "aaa", 100, recs...)})
	s := e.summary(alice, PeriodWeek, "UTC", now)
	for _, b := range s.Series {
		want := b.Date.After(ymd(2026, 11, 2))
		if b.NoData != want {
			t.Errorf("%s: no_data %v", b.Date.Format(time.DateOnly), b.NoData)
		}
	}
	if y := e.summary(alice, PeriodYear, "UTC", now); slices.ContainsFunc(y.Series, func(b Bucket) bool { return b.NoData }) {
		t.Errorf("year: %v", y.Series)
	}

	bob := e.snapshot("bob", time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC), []statstest.Book{statBook(1, "A", "aaa", 100, recs...)})
	y := e.summary(bob, PeriodYear, "UTC", now)
	if !y.Series[11].NoData || y.Series[10].NoData {
		t.Errorf("year: Oct %+v, Nov %+v", y.Series[10], y.Series[11])
	}
	// В Москве снимок принят уже 1 ноября: ноябрь не пустой.
	if y := e.summary(bob, PeriodYear, "Europe/Moscow", now); y.Series[11].NoData {
		t.Errorf("Moscow: Nov %+v", y.Series[11])
	}

	carol := e.snapshot("carol", now, []statstest.Book{statBook(1, "A", "aaa", 100, recs...)})
	if s := e.summary(carol, PeriodWeek, "UTC", now); slices.ContainsFunc(s.Series, func(b Bucket) bool { return b.NoData }) {
		t.Errorf("fresh: %v", s.Series)
	}
}

func TestCalendar(t *testing.T) {
	e := newEnv(t)
	alice := e.snapshot("alice", now, []statstest.Book{statBook(1, "A", "aaa", 100,
		pageRec(1, 100, unix(2025, 11, 2, 10, 0), 60),
		pageRec(1, 100, unix(2025, 11, 3, 10, 0), 60),
		pageRec(2, 100, unix(2025, 11, 3, 11, 0), 30),
		pageRec(1, 100, unix(2026, 6, 1, 10, 0), 0),
		pageRec(1, 100, unix(2026, 11, 4, 10, 0), 90),
		pageRec(1, 100, unix(2026, 11, 5, 10, 0), 90),
	)})
	cal, err := e.s.Calendar(ctx, alice, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if !cal.From.Equal(ymd(2025, 11, 3)) || cal.From.Weekday() != time.Monday || !cal.To.Equal(ymd(2026, 11, 4)) || cal.AsOf == nil {
		t.Fatalf("calendar %s–%s, as_of %v", cal.From, cal.To, cal.AsOf)
	}
	want := []Day{{ymd(2025, 11, 3), 90}, {ymd(2026, 11, 4), 90}}
	if !slices.EqualFunc(cal.Days, want, func(a, b Day) bool { return a.Date.Equal(b.Date) && a.Duration == b.Duration }) {
		t.Fatalf("days %v", cal.Days)
	}
}

func TestSessionsPaging(t *testing.T) {
	e := newEnv(t)
	t0 := unix(2026, 11, 2, 18, 0)
	// Сессии B и C начались в одну секунду (две читалки). У Z start_time = 0 —
	// значение по умолчанию колонки KOReader, у N часы читалки ушли до 1970-го:
	// курсор на таких сессиях не должен ломаться.
	alice := e.snapshot("alice", now, []statstest.Book{
		statBook(1, "A", "aaa", 100, pageRec(1, 100, t0, 60)),
		statBook(5, "Z", "zzz", 100, pageRec(1, 100, 0, 60)),
		statBook(6, "N", "nnn", 100, pageRec(1, 100, -5000, 60)),
		statBook(2, "B", "bbb", 100, pageRec(1, 100, t0+5000, 60)),
		statBook(3, "C", "ccc", 100, pageRec(1, 100, t0+5000, 60)),
		statBook(4, "D", "", 100, pageRec(1, 100, t0+5000, 60)),
	})
	all := e.allSessions(alice)
	if len(all) != 6 || *all[3].Book.Title != "A" || *all[4].Book.Title != "Z" || *all[5].Book.Title != "N" {
		t.Fatalf("sessions %v", all)
	}
	for _, limit := range []int{1, 2, 3, 4} {
		var got []Session
		var after *SessionCursor
		for range 10 {
			page, next, err := e.s.Sessions(ctx, alice, nil, after, limit)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, page...)
			if next == nil {
				break
			}
			parsed, err := ParseSessionCursor(next.String())
			if err != nil || parsed != *next {
				t.Fatalf("cursor %q: %v %v", next, parsed, err)
			}
			after = &parsed
		}
		if !slices.EqualFunc(got, all, func(a, b Session) bool {
			return *a.Book.Title == *b.Book.Title && a.StartedAt.Equal(b.StartedAt) && a.EndedAt.Equal(b.EndedAt)
		}) {
			t.Fatalf("limit %d: %v, want %v", limit, got, all)
		}
	}
}

func TestParseSessionCursor(t *testing.T) {
	for _, s := range []string{"", "abc", "17", "17.", ".5", "17.x", "17.0", "1.2.3"} {
		if _, err := ParseSessionCursor(s); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%q: %v", s, err)
		}
	}
	if c, err := ParseSessionCursor("1762000000.-7"); err != nil || c != (SessionCursor{1762000000, -7}) {
		t.Errorf("negative key: %v %v", c, err)
	}
	for _, s := range []string{"0.5", "-1.5"} {
		if _, err := ParseSessionCursor(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

// TestOracleDays сверяет дни с KOReader на настоящем statistics.sqlite3 (путь — в
// KSB_ORACLE_STATS): страницы по дням — с getDaysFromPeriod по вьюхе page_stat
// (в UTC, без 'localtime'), время — с суммой сырых duration.
// oracle открывает копию statistics.sqlite3 из KSB_ORACLE_STATS со вьюхой page_stat
// из statstest.Schema и возвращает базу и путь к копии.
func oracle(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := os.Getenv("KSB_ORACLE_STATS")
	if path == "" {
		t.Skip("KSB_ORACLE_STATS not set")
	}
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	copyPath := filepath.Join(t.TempDir(), "statistics.sqlite3")
	dst, err := os.Create(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()
	kodb, err := sql.Open("sqlite", "file:"+copyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kodb.Close() })
	for _, q := range []string{`DROP VIEW IF EXISTS page_stat`, `DROP TABLE IF EXISTS numbers`, statstest.Schema} {
		if _, err := kodb.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return kodb, copyPath
}

func TestOracleDays(t *testing.T) {
	kodb, copyPath := oracle(t)
	type dayStat struct {
		pages           int
		viewDur, rawDur int64
	}
	ko := map[string]*dayStat{}
	each := func(q string, scan func(rows *sql.Rows) error) {
		t.Helper()
		rows, err := kodb.QueryContext(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			if err := scan(rows); err != nil {
				t.Fatal(err)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	each(`
		SELECT dates, count(*), sum(sum_duration)
		FROM (
			SELECT strftime('%Y-%m-%d', start_time, 'unixepoch') AS dates, sum(duration) AS sum_duration
			FROM page_stat
			GROUP BY id_book, page, dates
		)
		GROUP BY dates`, func(rows *sql.Rows) error {
		var d string
		st := &dayStat{}
		if err := rows.Scan(&d, &st.pages, &st.viewDur); err != nil {
			return err
		}
		ko[d] = st
		return nil
	})
	each(`
		SELECT strftime('%Y-%m-%d', start_time, 'unixepoch'), sum(duration)
		FROM page_stat_data JOIN book ON book.id = id_book
		WHERE pages IS NOT NULL AND total_pages <> 0
		GROUP BY 1`, func(rows *sql.Rows) error {
		var d string
		var dur int64
		if err := rows.Scan(&d, &dur); err != nil {
			return err
		}
		if ko[d] == nil {
			return fmt.Errorf("%s: raw records without page_stat rows", d)
		}
		ko[d].rawDur = dur
		return nil
	})
	if err := kodb.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	alice := e.user("alice")
	if _, err := e.s.Import(ctx, alice, data); err != nil {
		t.Fatal(err)
	}
	rep, err := e.s.load(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	ours := map[string]*dayStat{}
	seen := map[dayPage]struct{}{}
	for _, r := range rep.records {
		d := date(time.Unix(r.start, 0), time.UTC)
		k := d.Format(time.DateOnly)
		st := ours[k]
		if st == nil {
			st = &dayStat{}
			ours[k] = st
		}
		st.rawDur += r.duration
		// Вьюха делит время записи на число её страниц нацело и теряет остаток.
		n := r.last - r.first + 1
		st.viewDur += n * (r.duration / n)
		for p := r.first; p <= r.last; p++ {
			if _, ok := seen[dayPage{d, r.key, p}]; !ok {
				seen[dayPage{d, r.key, p}] = struct{}{}
				st.pages++
			}
		}
	}
	var loss int64
	if len(ours) != len(ko) {
		t.Fatalf("%d days, KOReader %d", len(ours), len(ko))
	}
	for d, k := range ko {
		o := ours[d]
		if o == nil || o.pages != k.pages || o.rawDur != k.rawDur || o.viewDur != k.viewDur {
			t.Errorf("%s: ours %+v, KOReader %+v", d, o, k)
		}
		loss += k.rawDur - k.viewDur
	}
	t.Logf("%d days match, page_stat loses %d s", len(ko), loss)
}

func (e *env) book(userID int64, hash string, percentage float64, tz string) (*time.Time, *BookStats) {
	e.t.Helper()
	asOf, st, err := e.s.Book(ctx, userID, hash, percentage, mustLoc(e.t, tz))
	if err != nil {
		e.t.Fatal(err)
	}
	return asOf, st
}

func TestBook(t *testing.T) {
	e := newEnv(t)
	// 3 ноября 21:30 UTC — уже 4-е в Москве.
	late, day := unix(2026, 11, 3, 21, 30), unix(2026, 11, 4, 10, 0)
	alice := e.snapshot("alice", now, []statstest.Book{
		statBook(1, "A", "aaa", 100, pageRec(10, 100, late, 2400), pageRec(10, 100, day, 600), pageRec(11, 100, day+600, 600)),
		statBook(2, "B", "bbb", 100, pageRec(1, 100, day+3000, 60)),
	})
	asOf, st := e.book(alice, "aaa", 0.5, "UTC")
	if asOf == nil || !asOf.Equal(now) || st == nil {
		t.Fatalf("as_of %v, stats %+v", asOf, st)
	}
	if st.Duration != 3600 || st.Days != 2 || st.PagesRead != 2 || st.Sessions != 2 {
		t.Fatalf("stats %+v", st)
	}
	if !st.FirstReadAt.Equal(time.Unix(late, 0)) || !st.LastReadAt.Equal(time.Unix(day+1200, 0)) {
		t.Fatalf("first %v, last %v", st.FirstReadAt, st.LastReadAt)
	}
	if _, st := e.book(alice, "aaa", 0.5, "Europe/Moscow"); st.Days != 1 {
		t.Fatalf("Moscow days %d", st.Days)
	}
}

func TestBookPageTime(t *testing.T) {
	e := newEnv(t)
	day := unix(2026, 11, 4, 8, 0)
	alice := e.snapshot("alice", now, []statstest.Book{
		// Страница 1 — 300 с за две записи, ограничивается 120; страница 2 — 60.
		statBook(1, "Capped", "aaa", 2, pageRec(1, 2, day, 200), pageRec(2, 2, day+200, 60), pageRec(1, 2, day+260, 100)),
		// Запись другой разметки делится между страницами 9 и 10 нацело, как во вьюхе.
		statBook(2, "Split", "bbb", 200, pageRec(5, 100, day+1000, 61)),
	})
	_, st := e.book(alice, "aaa", 0.5, "UTC")
	if st.Duration != 360 || st.PageTime == nil || *st.PageTime != 90 {
		t.Fatalf("capped: %+v", st)
	}
	_, st = e.book(alice, "bbb", 0.5, "UTC")
	if st.Duration != 61 || st.PagesRead != 2 || st.PageTime == nil || *st.PageTime != 30 {
		t.Fatalf("split: %+v", st)
	}
}

func TestBookPages(t *testing.T) {
	e := newEnv(t)
	day := unix(2026, 11, 4, 8, 0)
	row := func(id int64, title, md5 string, pages, lastOpen int64, recs ...statstest.Record) statstest.Book {
		b := statBook(id, title, md5, pages, recs...)
		b.LastOpen = lastOpen
		return b
	}
	yesterday, today := unix(2026, 11, 3, 8, 0), unix(2026, 11, 4, 8, 0)
	alice := e.snapshot("alice", now, []statstest.Book{
		row(1, "H old", "hhh", 300, yesterday, pageRec(1, 300, day, 60)),
		row(2, "H new", "hhh", 320, today, pageRec(2, 320, day+60, 60)),
		row(3, "Z zero", "zzz", 0, today, pageRec(1, 100, day+200, 60)),
		row(4, "Z old", "zzz", 300, yesterday, pageRec(1, 300, day+300, 60)),
		row(5, "N zero", "nnn", 0, today, pageRec(1, 100, day+400, 60)),
		row(6, "N null", "nnn", 300, yesterday, pageRec(1, 300, day+500, 60)),
	}, `UPDATE book SET pages = NULL WHERE id = 6`)
	if _, st := e.book(alice, "hhh", 0.5, "UTC"); st.Pages == nil || *st.Pages != 320 || st.Duration != 120 || st.PagesRead != 2 {
		t.Fatalf("hhh: %+v", st)
	}
	if _, st := e.book(alice, "zzz", 0.5, "UTC"); st.Pages == nil || *st.Pages != 300 {
		t.Fatalf("zzz: %+v", st)
	}
	_, st := e.book(alice, "nnn", 0.5, "UTC")
	if st.Pages != nil || st.CurrentPage != nil || st.TimeLeft != nil || st.Duration != 60 || st.PageTime == nil {
		t.Fatalf("nnn: %+v", st)
	}
}

func TestBookMissing(t *testing.T) {
	e := newEnv(t)
	bob := e.user("bob")
	if asOf, st := e.book(bob, "aaa", 0.5, "UTC"); asOf != nil || st != nil {
		t.Fatalf("no snapshot: %v, %+v", asOf, st)
	}
	alice := e.snapshot("alice", now, []statstest.Book{
		statBook(1, "A", "aaa", 100, pageRec(1, 0, unix(2026, 11, 4, 8, 0), 60)),
		statBook(2, "B", "bbb", 100, pageRec(1, 100, unix(2026, 11, 4, 9, 0), 60)),
	})
	if asOf, st := e.book(alice, "ccc", 0.5, "UTC"); asOf == nil || st != nil {
		t.Fatalf("not in snapshot: %v, %+v", asOf, st)
	}
	// Единственная запись отброшена (total_pages = 0): книга есть, цифры нулевые.
	_, st := e.book(alice, "aaa", 0.5, "UTC")
	if st == nil || st.Duration != 0 || st.PagesRead != 0 || st.Days != 0 || st.Sessions != 0 ||
		st.FirstReadAt != nil || st.LastReadAt != nil || st.PageTime != nil || st.TimeLeft != nil ||
		st.Pages == nil || *st.Pages != 100 || st.CurrentPage == nil || *st.CurrentPage != 50 {
		t.Fatalf("no records: %+v", st)
	}
}

func TestBookSessionsAmongOthers(t *testing.T) {
	e := newEnv(t)
	t0 := unix(2026, 11, 4, 8, 0)
	alice := e.snapshot("alice", now, []statstest.Book{
		statBook(1, "A", "aaa", 100, pageRec(1, 100, t0, 60), pageRec(2, 100, t0+300, 60)),
		statBook(2, "B", "bbb", 100, pageRec(1, 100, t0+180, 60)),
	})
	if _, st := e.book(alice, "aaa", 0.5, "UTC"); st.Sessions != 2 {
		t.Fatalf("sessions %d", st.Sessions)
	}
	// Указатели BookRef у разных загрузок разные: сравниваются границы сессий.
	bounds := func(ss []Session) []Session {
		out := make([]Session, len(ss))
		for i, s := range ss {
			out[i] = Session{StartedAt: s.StartedAt, EndedAt: s.EndedAt, Duration: s.Duration, Pages: s.Pages, key: s.key}
		}
		return out
	}
	all := bounds(e.allSessions(alice))
	hash := "aaa"
	got, next, err := e.s.Sessions(ctx, alice, &hash, nil, 1000)
	got = bounds(got)
	if err != nil || next != nil || len(got) != 2 || !slices.Equal([]Session{all[0], all[2]}, got) {
		t.Fatalf("book sessions %+v, next %v, %v; all %+v", got, next, err, all)
	}
	// Подгрузка по одной: курсор общий со списком без фильтра.
	first, after, err := e.s.Sessions(ctx, alice, &hash, nil, 1)
	if err != nil || len(first) != 1 || after == nil {
		t.Fatalf("first page %+v, %v, %v", first, after, err)
	}
	second, next, err := e.s.Sessions(ctx, alice, &hash, after, 1)
	if err != nil || next != nil || !slices.Equal(bounds(append(first, second...)), got) {
		t.Fatalf("second page %+v, %v, %v", second, next, err)
	}
	missing := "ccc"
	if got, next, err := e.s.Sessions(ctx, alice, &missing, nil, 10); err != nil || next != nil || len(got) != 0 {
		t.Fatalf("missing hash: %+v, %v, %v", got, next, err)
	}
}

func TestEstimate(t *testing.T) {
	pt := 30.0
	for _, c := range []struct {
		pct      float64
		pages    int
		page     int
		timeLeft int64
		finished bool
	}{
		{0.624, 386, 241, 145 * 30, false},
		{0.0005, 300, 1, 299 * 30, false},
		{0.9996, 300, 300, 0, true},
		{0.9994, 300, 300, 0, false},
	} {
		st := BookStats{Pages: &c.pages, PageTime: &pt}
		st.estimate(c.pct)
		if st.CurrentPage == nil || *st.CurrentPage != c.page {
			t.Errorf("%v × %d: page %v, want %d", c.pct, c.pages, st.CurrentPage, c.page)
		}
		switch {
		case c.finished && st.TimeLeft != nil:
			t.Errorf("%v: time left %d for finished book", c.pct, *st.TimeLeft)
		case !c.finished && (st.TimeLeft == nil || *st.TimeLeft != c.timeLeft):
			t.Errorf("%v: time left %v, want %d", c.pct, st.TimeLeft, c.timeLeft)
		}
	}
}

// TestOracleBooks сверяет статистику книг с экраном книги KOReader на настоящем
// statistics.sqlite3 (KSB_ORACLE_STATS) у хешей с одной строкой book: «Pages read» —
// count(DISTINCT page) по page_stat, «Days reading this book» — число дат (в UTC),
// среднее на страницу — STATISTICS_SQL_BOOK_CAPPED_TOTALS_QUERY с max_sec = 120. Время
// не меньше суммы по вьюхе: она теряет остаток деления.
func TestOracleBooks(t *testing.T) {
	kodb, copyPath := oracle(t)
	type koBook struct {
		md5             string
		pages, days     int
		capped, viewDur int64
		cappedPages     int
	}
	var books []koBook
	rows, err := kodb.QueryContext(ctx, `
		SELECT b.id, b.md5 FROM book b
		WHERE b.md5 IS NOT NULL AND (SELECT count(*) FROM book o WHERE o.md5 = b.md5) = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	var md5s []string
	for rows.Next() {
		var id int64
		var md5 string
		if err := rows.Scan(&id, &md5); err != nil {
			t.Fatal(err)
		}
		ids, md5s = append(ids, id), append(md5s, md5)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Закрыть до следующих запросов, не дожидаясь defer.
	rows.Close()
	for i, id := range ids {
		b := koBook{md5: md5s[i]}
		if err := kodb.QueryRowContext(ctx, `
			SELECT count(DISTINCT page), count(DISTINCT strftime('%Y-%m-%d', start_time, 'unixepoch')),
			       coalesce(sum(duration), 0)
			FROM page_stat WHERE id_book = ?`, id).Scan(&b.pages, &b.days, &b.viewDur); err != nil {
			t.Fatal(err)
		}
		if err := kodb.QueryRowContext(ctx, `
			SELECT count(*), coalesce(sum(durations), 0)
			FROM (SELECT min(sum(duration), ?) AS durations FROM page_stat WHERE id_book = ? GROUP BY page)`,
			maxPageTime, id).Scan(&b.cappedPages, &b.capped); err != nil {
			t.Fatal(err)
		}
		books = append(books, b)
	}
	if err := kodb.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	alice := e.user("alice")
	if _, err := e.s.Import(ctx, alice, data); err != nil {
		t.Fatal(err)
	}
	var loss int64
	for _, k := range books {
		_, st := e.book(alice, k.md5, 0.5, "UTC")
		if st == nil {
			t.Errorf("%s: no stats", k.md5)
			continue
		}
		if st.PagesRead != k.pages || st.Days != k.days || st.Duration < k.viewDur {
			t.Errorf("%s: ours %+v, KOReader pages %d, days %d, view time %d", k.md5, st, k.pages, k.days, k.viewDur)
		}
		switch {
		case k.cappedPages == 0 && st.PageTime != nil:
			t.Errorf("%s: page time %v without pages", k.md5, *st.PageTime)
		case k.cappedPages > 0 && (st.PageTime == nil || *st.PageTime != float64(k.capped)/float64(k.cappedPages)):
			t.Errorf("%s: page time %v, KOReader %d/%d", k.md5, st.PageTime, k.capped, k.cappedPages)
		}
		loss += st.Duration - k.viewDur
	}
	t.Logf("%d books match, page_stat loses %d s", len(books), loss)
}

func TestBookNegativeStart(t *testing.T) {
	e := newEnv(t)
	alice := e.snapshot("alice", now, []statstest.Book{statBook(1, "A", "aaa", 100, pageRec(1, 100, -500, 60))})
	if _, st := e.book(alice, "aaa", 0.5, "UTC"); st.LastReadAt == nil || st.LastReadAt.Unix() != -440 || st.FirstReadAt.Unix() != -500 {
		t.Fatalf("first %v, last %v", st.FirstReadAt, st.LastReadAt)
	}
}
