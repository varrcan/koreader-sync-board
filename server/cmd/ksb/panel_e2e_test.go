package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics/statstest"
)

// panel — браузер панели со своим cookie jar и адресом за доверенным прокси.
type panel struct {
	t    *testing.T
	base string
	ip   string
	c    *http.Client
}

func newPanel(t *testing.T, base, ip string) panel {
	jar, _ := cookiejar.New(nil)
	return panel{t: t, base: base, ip: ip, c: &http.Client{Jar: jar}}
}

func (p panel) call(method, path string, body any) (int, map[string]any) {
	p.t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, p.base+path, bytes.NewReader(data))
	if err != nil {
		p.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Forwarded-For", p.ip)
	res, err := p.c.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// TestPanelE2E: пустая база без ADMIN_* → код в логе → мастер → me → выход,
// затем общий с kosync бюджет неудачных входов.
func TestPanelE2E(t *testing.T) {
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_USER", "")
	t.Setenv("ADMIN_PASSWORD", "")
	base, logs := startServer(t)

	m := regexp.MustCompile(`"msg":"setup required","setup_code":"([a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4})"`).FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatalf("no setup code in log:\n%s", logs)
	}
	b := newPanel(t, base, "198.51.100.20")
	if code, body := b.call("GET", "/api/v1/setup", nil); code != 200 || body["required"] != true {
		t.Fatalf("setup status: %d %v", code, body)
	}
	if code, _ := b.call("GET", "/api/v1/me", nil); code != 401 {
		t.Fatalf("me before setup: %d", code)
	}
	code, body := b.call("POST", "/api/v1/setup", map[string]string{"code": m[1], "username": "admin", "password": "admin-password"})
	if code != 200 || body["role"] != "admin" {
		t.Fatalf("setup: %d %v", code, body)
	}
	if code, body := b.call("GET", "/api/v1/me", nil); code != 200 || body["username"] != "admin" {
		t.Fatalf("me: %d %v", code, body)
	}
	if code, body := b.call("GET", "/api/v1/setup", nil); code != 200 || body["required"] != false {
		t.Fatalf("setup after: %d %v", code, body)
	}
	if code, _ := b.call("POST", "/api/v1/auth/logout", nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := b.call("GET", "/api/v1/me", nil); code != 401 {
		t.Fatalf("me after logout: %d", code)
	}

	// Подбор пароля панели блокирует адрес и для kosync.
	const attacker = "198.51.100.66"
	guess := newPanel(t, base, attacker)
	for i := range 10 {
		if code, body := guess.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": fmt.Sprint("guess", i)}); code != 401 {
			t.Fatalf("guess %d: %d %v", i, code, body)
		}
	}
	if code, _ := guess.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password"}); code != 429 {
		t.Fatalf("panel from blocked address: %d", code)
	}
	reader{t: t, base: base, ip: attacker, user: "admin", password: "anything"}.login().
		expect(t, "kosync from blocked address", 401, "Too many failed attempts. Try again in 15 minutes.")
	// Соседний адрес не затронут.
	if code, _ := newPanel(t, base, "198.51.100.21").call("POST", "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": "admin-password"}); code != 200 {
		t.Fatalf("login from another address: %d", code)
	}
	if n := regexp.MustCompile(`"msg":"panel: authorization failed","user":"admin","ip":"198\.51\.100\.66","path":"/api/v1/auth/login"`).
		FindAllStringIndex(logs.String(), -1); len(n) != 10 {
		t.Fatalf("%d panel failure lines:\n%s", len(n), logs)
	}
}

// TestPanelDevicesE2E: устройство из панели → читалка синхронизируется →
// панель видит, что она сообщила → новый пароль → отзыв администратором.
func TestPanelDevicesE2E(t *testing.T) {
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_USER", "")
	t.Setenv("ADMIN_PASSWORD", "")
	panelPassword := func(args ...string) string {
		t.Helper()
		var out, errb bytes.Buffer
		if code := run(append([]string{"user", "add"}, args...), &out, &errb); code != 0 {
			t.Fatalf("user add: %s", errb.String())
		}
		if code := run([]string{"user", "passwd", args[0]}, &out, &errb); code != 0 {
			t.Fatalf("user passwd: %s", errb.String())
		}
		m := regexp.MustCompile(`(?m)^  password: (\S+)$`).FindStringSubmatch(out.String())
		if m == nil {
			t.Fatalf("no panel password: %s", out.String())
		}
		return m[1]
	}
	alicePass, rootPass := panelPassword("alice"), panelPassword("root", "-role", "admin")
	base, _ := startServer(t)

	alice := newPanel(t, base, "198.51.100.30")
	if code, body := alice.call("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": alicePass}); code != 200 {
		t.Fatalf("alice login: %d %v", code, body)
	}
	code, body := alice.call("POST", "/api/v1/devices", map[string]string{"name": "Kobo"})
	if code != 201 {
		t.Fatalf("create device: %d %v", code, body)
	}
	id := fmt.Sprintf("/api/v1/devices/%.0f", body["device"].(map[string]any)["id"])
	kobo := reader{t: t, base: base, ip: "198.51.100.31", user: "alice", password: body["password"].(string)}
	kobo.login().expect(t, "reader login", 200, "")
	kobo.push(accounts.KeyFromPassword("e2e book"), 0.5).expect(t, "push", 200, "")

	reported := func() any {
		t.Helper()
		res, err := alice.c.Get(base + "/api/v1/devices")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var list []map[string]any
		if err := json.NewDecoder(res.Body).Decode(&list); err != nil || len(list) != 1 {
			t.Fatalf("list: %v %v", list, err)
		}
		return list[0]["reported_device"]
	}
	if r := reported(); r != "e2e" {
		t.Fatalf("reported_device = %v", r)
	}

	code, body = alice.call("POST", id+"/password", nil)
	if code != 200 {
		t.Fatalf("reset: %d %v", code, body)
	}
	rotated := kobo
	rotated.password = body["password"].(string)
	unauthorized := func(name string, r reader) {
		t.Helper()
		if a := r.login(); a.status != 401 || a.body["code"] != float64(2001) {
			t.Fatalf("%s: %+v", name, a)
		}
	}
	unauthorized("old key after reset", kobo)
	rotated.login().expect(t, "new key", 200, "")

	root := newPanel(t, base, "198.51.100.32")
	if code, _ := root.call("POST", "/api/v1/auth/login", map[string]string{"username": "root", "password": rootPass}); code != 200 {
		t.Fatalf("root login: %d", code)
	}
	if code, body := root.call("DELETE", id, nil); code != 204 {
		t.Fatalf("revoke: %d %v", code, body)
	}
	unauthorized("revoked", rotated)
}

// TestPanelProgressE2E: читалка шлёт прогресс с metadata и без → панель видит
// книгу с названием и неизвестную книгу, ленту и устройства книги.
func TestPanelProgressE2E(t *testing.T) {
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "admin-password")
	base, _ := startServer(t)

	admin := newPanel(t, base, "198.51.100.40")
	if code, body := admin.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password"}); code != 200 {
		t.Fatalf("login: %d %v", code, body)
	}
	code, body := admin.call("POST", "/api/v1/devices", map[string]string{"name": "pocketbook"})
	if code != 201 {
		t.Fatalf("create device: %d %v", code, body)
	}
	pb := reader{t: t, base: base, ip: "198.51.100.41", user: "admin", password: body["password"].(string)}
	known, unknown := accounts.KeyFromPassword("known book"), accounts.KeyFromPassword("unknown book")
	pb.call("PUT", "/syncs/progress", map[string]any{
		"document": known, "progress": "/body/DocFragment[5]/body/p[3]/text().0", "percentage": 0.3,
		"device": "PocketBook 740", "device_id": "PB",
		"metadata": map[string]any{"title": "Мастер и Маргарита", "authors": "Михаил Булгаков", "filename": "master.epub"},
	}).expect(t, "push with metadata", 200, "")
	pb.push(unknown, 0.1).expect(t, "push without metadata", 200, "")

	code, body = admin.call("GET", "/api/v1/books", nil)
	items, _ := body["items"].([]any)
	if code != 200 || len(items) != 2 {
		t.Fatalf("books: %d %v", code, body)
	}
	u, k := items[0].(map[string]any), items[1].(map[string]any)
	if u["hash"] != unknown || u["title"] != nil || u["filename"] != nil ||
		k["hash"] != known || k["title"] != "Мастер и Маргарита" || k["filename"] != "master.epub" || k["device_name"] != "pocketbook" {
		t.Fatalf("books %v", items)
	}
	if c := body["counts"].(map[string]any); c["unknown"] != 1.0 || c["reading"] != 2.0 {
		t.Fatalf("counts %v", c)
	}

	code, body = admin.call("GET", "/api/v1/activity?document="+known, nil)
	if items, _ := body["items"].([]any); code != 200 || len(items) != 1 || items[0].(map[string]any)["syncs"] != 1.0 {
		t.Fatalf("activity: %d %v", code, body)
	}
	code, body = admin.call("GET", "/api/v1/books/"+known, nil)
	ds, _ := body["devices"].([]any)
	if code != 200 || len(ds) != 1 || ds[0].(map[string]any)["reported_device"] != "PocketBook 740" {
		t.Fatalf("book: %d %v", code, body)
	}
}

// TestPanelStatisticsE2E: снимок загружается из панели → итоги недели и сессии.
func TestPanelStatisticsE2E(t *testing.T) {
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "admin-password")
	base, _ := startServer(t)

	admin := newPanel(t, base, "198.51.100.50")
	if code, body := admin.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password"}); code != 200 {
		t.Fatalf("login: %d %v", code, body)
	}
	// Две сессии: пауза между ними больше 10 минут.
	start := time.Now().Add(-3 * time.Hour).Unix()
	data := statstest.Build(t, []statstest.Book{{ID: 1, Title: "Пикник на обочине", Authors: "Стругацкие",
		MD5: "0123456789abcdef0123456789abcdef", Pages: 200, Records: []statstest.Record{
			{Page: 1, StartTime: start, Duration: 90, TotalPages: 200},
			{Page: 2, StartTime: start + 100, Duration: 90, TotalPages: 200},
			{Page: 3, StartTime: start + 3600, Duration: 90, TotalPages: 200},
		}}})
	req, _ := http.NewRequest("POST", base+"/api/v1/statistics/import", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Forwarded-For", admin.ip)
	res, err := admin.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("import: %d", res.StatusCode)
	}

	code, body := admin.call("GET", "/api/v1/statistics/summary?period=week&tz=Europe/Moscow", nil)
	if code != 200 {
		t.Fatalf("summary: %d %v", code, body)
	}
	tot := body["totals"].(map[string]any)
	books, _ := body["books"].([]any)
	if tot["duration"] != 270.0 || tot["pages"] != 3.0 || tot["sessions"] != 2.0 || len(books) != 1 ||
		books[0].(map[string]any)["title"] != "Пикник на обочине" || body["as_of"] == nil {
		t.Fatalf("summary %v", body)
	}
	code, body = admin.call("GET", "/api/v1/statistics/sessions", nil)
	items, _ := body["items"].([]any)
	if code != 200 || len(items) != 2 || items[0].(map[string]any)["duration"] != 90.0 || items[1].(map[string]any)["pages"] != 2.0 {
		t.Fatalf("sessions: %d %v", code, body)
	}
}
