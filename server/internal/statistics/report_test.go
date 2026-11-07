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
	out, next, err := e.s.Sessions(ctx, userID, nil, 1000)
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
			page, next, err := e.s.Sessions(ctx, alice, after, limit)
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
func TestOracleDays(t *testing.T) {
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
	defer kodb.Close()
	for _, q := range []string{`DROP VIEW IF EXISTS page_stat`, `DROP TABLE IF EXISTS numbers`, statstest.Schema} {
		if _, err := kodb.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
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
