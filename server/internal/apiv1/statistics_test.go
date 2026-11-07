package apiv1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics/statstest"
)

// upload отправляет файл так же, как панель: тело — сами байты.
func (b *browser) upload(data []byte) resp {
	b.e.t.Helper()
	req, _ := http.NewRequest("POST", b.e.url+"/api/v1/statistics/import", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer res.Body.Close()
	r := resp{status: res.StatusCode, header: res.Header}
	_ = json.NewDecoder(res.Body).Decode(&r.body)
	return r
}

func snapshotOf(t *testing.T, r resp) map[string]any {
	t.Helper()
	r.expect(t, "statistics", 200, "")
	s, ok := r.body["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot %v", r.body)
	}
	return s
}

func statsFile(t *testing.T, titles ...string) []byte {
	var books []statstest.Book
	for i, title := range titles {
		books = append(books, statstest.Book{ID: int64(i + 1), Title: title, Authors: "N/A", MD5: fmt.Sprintf("%032x", i+len(title)),
			Pages: 10, Records: []statstest.Record{{Page: 1, StartTime: int64(1000 * (i + 1)), Duration: 30, TotalPages: 10}}})
	}
	return statstest.Build(t, books)
}

func TestStatisticsWithoutSession(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.do("GET", "/api/v1/statistics", nil).expect(t, "get", 401, "unauthorized")
	b.upload(statsFile(t, "A")).expect(t, "import", 401, "unauthorized")
}

func TestStatisticsAPI(t *testing.T) {
	e := newEnv(t)
	e.admin()
	alice := e.panelUser("alice", accounts.RoleUser)
	r := alice.do("GET", "/api/v1/statistics", nil)
	r.expect(t, "empty", 200, "")
	if v, ok := r.body["snapshot"]; !ok || v != nil {
		t.Fatalf("empty snapshot: %v", r.body)
	}

	// Выгрузка с читалки.
	_, pass, err := e.acc.AddDevice(e.ctx, "alice", "pocketbook")
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.acc.Authenticate(e.ctx, "alice", accounts.KeyFromPassword(pass), client)
	if err != nil {
		t.Fatal(err)
	}
	data := statsFile(t, "A", "B")
	if _, _, err := e.stats.Save(e.ctx, p.UserID, &p, statistics.FileStatistics, data, statistics.Cond{}); err != nil {
		t.Fatal(err)
	}
	s := snapshotOf(t, alice.do("GET", "/api/v1/statistics", nil))
	if s["device_name"] != "pocketbook" || s["source"] != "webdav" || s["books"] != 2.0 || s["records"] != 2.0 ||
		s["size"] != float64(len(data)) || s["first_read_at"] != "1970-01-01T00:16:40Z" || s["last_read_at"] != "1970-01-01T00:33:20Z" {
		t.Fatalf("after webdav: %v", s)
	}

	// Импорт объединяется со снимком.
	s = snapshotOf(t, alice.upload(statsFile(t, "C")))
	if s["source"] != "import" || s["device_name"] != nil || s["books"] != 3.0 || s["records"] != 3.0 {
		t.Fatalf("after import: %v", s)
	}

	alice.upload([]byte("not a database")).expect(t, "not sqlite", 400, "invalid_statistics")
	alice.upload([]byte("SQLite format 3\x00 garbage")).expect(t, "broken", 400, "invalid_statistics")
	if s := snapshotOf(t, alice.do("GET", "/api/v1/statistics", nil)); s["books"] != 3.0 {
		t.Fatalf("rejected file changed statistics: %v", s)
	}

	// Администратор видит только свою статистику.
	admin := e.browser()
	admin.login("admin", "admin-password").expect(t, "login", 200, "")
	if r := admin.do("GET", "/api/v1/statistics", nil); r.body["snapshot"] != nil {
		t.Fatalf("admin sees %v", r.body)
	}
	// Импорт без снимка — файл становится снимком.
	s = snapshotOf(t, admin.upload(statsFile(t, "X")))
	if s["source"] != "import" || s["books"] != 1.0 {
		t.Fatalf("admin import: %v", s)
	}
}

func TestStatisticsImportTooLarge(t *testing.T) {
	e := newEnv(t)
	alice := e.panelUser("alice", accounts.RoleUser)
	big := make([]byte, statistics.MaxSize+1)
	alice.upload(big).expect(t, "too large", 413, "file_too_large")
}

func TestStatisticsImportConflict(t *testing.T) {
	var e *env
	var p accounts.Principal
	n := 0
	e = newEnv(t, statistics.WithBeforeImportSave(func() {
		// Читалка выгружает новый снимок перед каждой записью импорта.
		n++
		if _, _, err := e.stats.Save(e.ctx, p.UserID, &p, statistics.FileStatistics, statsFile(e.t, fmt.Sprint("R", n)),
			statistics.Cond{Match: []string{"*"}}); err != nil {
			e.t.Error(err)
		}
	}))
	alice := e.panelUser("alice", accounts.RoleUser)
	_, pass, err := e.acc.AddDevice(e.ctx, "alice", "kobo")
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.acc.Authenticate(e.ctx, "alice", accounts.KeyFromPassword(pass), client); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.stats.Save(e.ctx, p.UserID, &p, statistics.FileStatistics, statsFile(t, "A"), statistics.Cond{}); err != nil {
		t.Fatal(err)
	}
	alice.upload(statsFile(t, "C")).expect(t, "conflict", 409, "conflict")
}

// recentStats — снимок с записями за последние часы: обработчики считают «сегодня»
// по настоящим часам.
func recentStats(t *testing.T, title string, sessions int) []byte {
	t.Helper()
	base := time.Now().Add(-time.Duration(sessions) * time.Hour).Unix()
	var recs []statstest.Record
	for i := range sessions {
		recs = append(recs, statstest.Record{Page: int64(i + 1), StartTime: base + int64(i)*3600, Duration: 60, TotalPages: 10})
	}
	return statstest.Build(t, []statstest.Book{{ID: 1, Title: title, Authors: "N/A", MD5: fmt.Sprintf("%032x", len(title)),
		Pages: 10, Records: recs}})
}

func TestStatisticsAggregatesWithoutSession(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.do("GET", "/api/v1/statistics/summary?period=week&tz=UTC", nil).expect(t, "summary", 401, "unauthorized")
	b.do("GET", "/api/v1/statistics/calendar?tz=UTC", nil).expect(t, "calendar", 401, "unauthorized")
	b.do("GET", "/api/v1/statistics/sessions", nil).expect(t, "sessions", 401, "unauthorized")
}

func TestStatisticsAggregatesBadRequest(t *testing.T) {
	e := newEnv(t)
	e.admin()
	alice := e.panelUser("alice", accounts.RoleUser)
	for _, q := range []string{
		"/api/v1/statistics/summary?period=week",
		"/api/v1/statistics/summary?period=week&tz=",
		"/api/v1/statistics/summary?period=week&tz=Local",
		"/api/v1/statistics/summary?period=week&tz=Mars/Olympus",
		"/api/v1/statistics/summary?period=decade&tz=UTC",
		"/api/v1/statistics/summary?tz=UTC",
		"/api/v1/statistics/calendar",
		"/api/v1/statistics/calendar?tz=Local",
		"/api/v1/statistics/sessions?limit=0",
		"/api/v1/statistics/sessions?limit=101",
		"/api/v1/statistics/sessions?cursor=17",
		"/api/v1/statistics/sessions?cursor=abc.def",
		"/api/v1/statistics/sessions?cursor=17.0",
	} {
		alice.do("GET", q, nil).expect(t, q, 400, "bad_request")
	}
}

func TestStatisticsAggregatesEmpty(t *testing.T) {
	e := newEnv(t)
	e.admin()
	alice := e.panelUser("alice", accounts.RoleUser)
	r := alice.do("GET", "/api/v1/statistics/summary?period=week&tz=Europe/Moscow", nil)
	r.expect(t, "summary", 200, "")
	totals := r.body["totals"].(map[string]any)
	if v, ok := r.body["as_of"]; !ok || v != nil || totals["duration"] != 0.0 || len(r.body["books"].([]any)) != 0 ||
		len(r.body["series"].([]any)) != 7 || len(r.body["hours"].([]any)) != 24 || r.body["bucket"] != "day" {
		t.Fatalf("summary %v", r.body)
	}
	r = alice.do("GET", "/api/v1/statistics/calendar?tz=UTC", nil)
	r.expect(t, "calendar", 200, "")
	if days, ok := r.body["days"].([]any); !ok || len(days) != 0 {
		t.Fatalf("calendar %v", r.body)
	}
	r = alice.do("GET", "/api/v1/statistics/sessions", nil)
	r.expect(t, "sessions", 200, "")
	if items, ok := r.body["items"].([]any); !ok || len(items) != 0 || r.body["next_cursor"] != nil {
		t.Fatalf("sessions %v", r.body)
	}
}

func TestStatisticsAggregates(t *testing.T) {
	e := newEnv(t)
	e.admin()
	admin := e.browser()
	admin.login("admin", "admin-password").expect(t, "login", 200, "")
	alice := e.panelUser("alice", accounts.RoleUser)
	admin.upload(recentStats(t, "Admin book", 1)).expect(t, "admin import", 200, "")
	alice.upload(recentStats(t, "Alice book", 3)).expect(t, "alice import", 200, "")

	r := admin.do("GET", "/api/v1/statistics/summary?period=year&tz=UTC", nil)
	r.expect(t, "summary", 200, "")
	books := r.body["books"].([]any)
	if len(books) != 1 || books[0].(map[string]any)["title"] != "Admin book" || r.body["bucket"] != "month" ||
		len(r.body["series"].([]any)) != 12 {
		t.Fatalf("admin summary %v", r.body)
	}

	r = alice.do("GET", "/api/v1/statistics/summary?period=week&tz=UTC", nil)
	r.expect(t, "summary", 200, "")
	today := time.Now().UTC().Format(time.DateOnly)
	if r.body["to"] != today || r.body["period"] != "week" {
		t.Fatalf("summary dates %v", r.body)
	}
	if _, err := time.Parse(time.RFC3339, r.body["as_of"].(string)); err != nil {
		t.Fatalf("as_of %v", r.body["as_of"])
	}
	first := r.body["series"].([]any)[0].(map[string]any)
	if _, err := time.Parse(time.DateOnly, first["date"].(string)); err != nil || first["no_data"] != false {
		t.Fatalf("bucket %v", first)
	}
	if tot := r.body["totals"].(map[string]any); tot["duration"] != 180.0 || tot["sessions"] != 3.0 || tot["pages"] != 3.0 {
		t.Fatalf("totals %v", tot)
	}

	// Сессии по одной: курсор на каждой странице, кроме последней.
	var titles []string
	path := "/api/v1/statistics/sessions?limit=2"
	for range 5 {
		r = alice.do("GET", path, nil)
		r.expect(t, "sessions", 200, "")
		for _, it := range r.body["items"].([]any) {
			s := it.(map[string]any)
			started, err := time.Parse(time.RFC3339, s["started_at"].(string))
			if err != nil || started.Location() != time.UTC {
				t.Fatalf("started_at %v", s["started_at"])
			}
			titles = append(titles, s["book"].(map[string]any)["title"].(string))
		}
		next, ok := r.body["next_cursor"].(string)
		if !ok {
			break
		}
		path = "/api/v1/statistics/sessions?limit=2&cursor=" + url.QueryEscape(next)
	}
	if len(titles) != 3 || r.body["next_cursor"] != nil {
		t.Fatalf("sessions %v, last %v", titles, r.body)
	}
}
