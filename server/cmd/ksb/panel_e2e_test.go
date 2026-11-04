package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"testing"
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
