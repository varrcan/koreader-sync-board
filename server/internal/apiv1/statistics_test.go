package apiv1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

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
