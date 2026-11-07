package main

import (
	"bytes"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/sso/ssotest"
)

// follow проходит цепочку перенаправлений браузера через API и провайдера и
// возвращает адрес страницы панели, на которую она привела.
func (p panel) follow(target string) string {
	p.t.Helper()
	c := &http.Client{Jar: p.c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	base, _ := url.Parse(p.base)
	next, err := base.Parse(target)
	if err != nil {
		p.t.Fatal(err)
	}
	for range 10 {
		req, _ := http.NewRequest("GET", next.String(), nil)
		req.Header.Set("X-Forwarded-For", p.ip)
		res, err := c.Do(req)
		if err != nil {
			p.t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusFound {
			p.t.Fatalf("%s: status %d", next, res.StatusCode)
		}
		if next, err = next.Parse(res.Header.Get("Location")); err != nil {
			p.t.Fatal(err)
		}
		if next.Host == base.Host && !strings.HasPrefix(next.Path, "/api/") {
			return next.RequestURI()
		}
	}
	p.t.Fatal("redirect loop")
	return ""
}

// TestOIDCE2E: вход паролем → привязка → выход → OIDC-вход тем же
// пользователем; автосоздание второго пользователя.
func TestOIDCE2E(t *testing.T) {
	idp := ssotest.New(t, "ksb")
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "admin-password")
	t.Setenv("KSB_OIDC_ISSUER", idp.URL)
	t.Setenv("KSB_OIDC_CLIENT_ID", "ksb")
	t.Setenv("KSB_OIDC_AUTO_CREATE", "true")
	t.Setenv("KSB_PANEL_PASSWORD_LOGIN", "false")
	base, logs := startServer(t)
	if !strings.Contains(logs.String(), `"msg":"oidc enabled","issuer":"`+idp.URL+`"`) {
		t.Fatalf("no oidc start line:\n%s", logs)
	}

	b := newPanel(t, base, "198.51.100.30")
	if code, body := b.call("GET", "/api/v1/auth/methods", nil); code != 200 || body["password"] != "unlinked" || body["oidc"] == nil {
		t.Fatalf("methods: %d %v", code, body)
	}
	if code, _ := b.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password"}); code != 200 {
		t.Fatalf("password login: %d", code)
	}
	idp.SetClaims(map[string]any{"sub": "admin-sub", "preferred_username": "someone"})
	code, body := b.call("POST", "/api/v1/me/oidc", nil)
	au, _ := body["authorize_url"].(string)
	if code != 200 || au == "" {
		t.Fatalf("start link: %d %v", code, body)
	}
	if page := b.follow(au); page != "/account?oidc=linked" {
		t.Fatalf("link: %s", page)
	}
	if code, _ := b.call("POST", "/api/v1/auth/logout", nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code, body := b.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password"}); code != 403 || body["code"] != "password_login_disabled" {
		t.Fatalf("password after link: %d %v", code, body)
	}
	if page := b.follow("/api/v1/auth/oidc/login?redirect=/books"); page != "/books" {
		t.Fatalf("oidc login: %s", page)
	}
	if code, body := b.call("GET", "/api/v1/me", nil); code != 200 || body["username"] != "admin" || body["oidc_linked"] != true {
		t.Fatalf("me: %d %v", code, body)
	}

	idp.SetClaims(map[string]any{"sub": "bob-sub", "preferred_username": "bob"})
	bob := newPanel(t, base, "198.51.100.31")
	if page := bob.follow("/api/v1/auth/oidc/login?redirect=/"); page != "/" {
		t.Fatalf("auto create: %s", page)
	}
	if code, body := bob.call("GET", "/api/v1/me", nil); code != 200 || body["username"] != "bob" || body["role"] != "user" || body["has_password"] != false {
		t.Fatalf("bob me: %d %v", code, body)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"user", "list"}, &out, &errb); code != 0 ||
		!regexp.MustCompile(`(?m)^bob\s+user\s+linked\s`).MatchString(out.String()) {
		t.Fatalf("user list: %s%s", out.String(), errb.String())
	}
}

// TestOIDCUnavailableE2E: провайдер не отвечает при старте — сервер работает,
// kosync тоже, а OIDC-вход уводит на экран входа с oidc_unavailable.
func TestOIDCUnavailableE2E(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String()
	l.Close()
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := run([]string{"user", "add", "alice"}, &out, &errb); code != 0 {
		t.Fatalf("user add: %s", errb.String())
	}
	if code := run([]string{"device", "add", "alice", "Kindle"}, &out, &errb); code != 0 {
		t.Fatalf("device add: %s", errb.String())
	}
	m := regexp.MustCompile(`(?m)^  password: ([a-z2-7]{4}-[a-z2-7]{4})$`).FindStringSubmatch(out.String())
	t.Setenv("KSB_OIDC_ISSUER", dead)
	t.Setenv("KSB_OIDC_CLIENT_ID", "ksb")
	base, _ := startServer(t)

	kindle := reader{t: t, base: base, ip: "198.51.100.40", user: "alice", password: m[1]}
	kindle.login().expect(t, "kosync login", 200, "")
	kindle.push(accounts.KeyFromPassword("book"), 0.5).expect(t, "kosync push", 200, "")
	b := newPanel(t, base, "198.51.100.41")
	if page := b.follow("/api/v1/auth/oidc/login?redirect=/"); page != "/login?oidc_error=oidc_unavailable" {
		t.Fatalf("oidc login: %s", page)
	}
}
