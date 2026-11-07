package apiv1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

// lockedBuffer — лог сервера, в который пишут горутины httptest.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	t     *testing.T
	ctx   context.Context
	url   string
	db    *store.DB
	acc   *accounts.Service
	setup *panelauth.Setup
	stats *statistics.Store
	logs  *lockedBuffer
	srv   *Server
}

func newEnv(t *testing.T, statsOpts ...statistics.Option) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logs := &lockedBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	lim := ratelimit.New(log)
	acc := accounts.New(db, accounts.WithArgon2Params(accounts.TestArgon2Params), accounts.WithLimiter(lim))
	setup := panelauth.NewSetup(acc, lim)
	stats := statistics.New(db, t.TempDir(), log, statsOpts...)
	api := &Server{Accounts: acc, Auth: panelauth.New(acc, nil, log), Setup: setup,
		Progress: progress.New(db), Statistics: stats, Log: log}
	srv := httptest.NewServer(NewHandler(api))
	t.Cleanup(srv.Close)
	return &env{t: t, ctx: ctx, url: srv.URL, db: db, acc: acc, setup: setup, stats: stats, logs: logs, srv: api}
}

// browser — клиент со своим cookie jar.
type browser struct {
	e      *env
	c      *http.Client
	header map[string]string
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{e: e, c: &http.Client{Jar: jar}}
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	items  []any // тело-массив
}

func (b *browser) do(method, path string, body any) resp {
	b.e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, b.e.url+path, rd)
	for k, v := range b.header {
		req.Header.Set(k, v)
	}
	res, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer res.Body.Close()
	r := resp{status: res.StatusCode, header: res.Header}
	var v any
	_ = json.NewDecoder(res.Body).Decode(&v)
	r.body, _ = v.(map[string]any)
	r.items, _ = v.([]any)
	return r
}

func (r resp) expect(t *testing.T, name string, status int, code string) {
	t.Helper()
	if r.status != status || (code != "" && r.body["code"] != code) {
		t.Fatalf("%s: %d %v, want %d %q", name, r.status, r.body, status, code)
	}
}

func (b *browser) login(user, password string) resp {
	return b.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": password})
}

func (e *env) admin() {
	e.t.Helper()
	if _, err := e.acc.CreateFirstAdmin(e.ctx, "admin", "admin-password"); err != nil {
		e.t.Fatal(err)
	}
}

func TestPublicAndProtected(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	for _, p := range []string{"/api/v1/health", "/api/v1/version", "/api/v1/setup"} {
		b.do("GET", p, nil).expect(t, p, 200, "")
	}
	b.do("POST", "/api/v1/auth/logout", nil).expect(t, "logout without session", 204, "")
	b.do("GET", "/api/v1/me", nil).expect(t, "me", 401, "unauthorized")
	b.do("PUT", "/api/v1/me/password", map[string]string{}).expect(t, "password", 401, "unauthorized")
	b.do("GET", "/api/v1/nope", nil).expect(t, "unknown path", 404, "not_found")
	b.do("DELETE", "/api/v1/me", nil).expect(t, "unknown method", 404, "not_found")
}

func TestLoginLogout(t *testing.T) {
	e := newEnv(t)
	e.admin()
	b := e.browser()
	r := b.login("admin", "admin-password")
	r.expect(t, "login", 200, "")
	if r.body["username"] != "admin" || r.body["role"] != "admin" {
		t.Fatalf("login body %v", r.body)
	}
	if c := r.header.Get("Set-Cookie"); !strings.Contains(c, "ksb_session=") || !strings.Contains(c, "HttpOnly") {
		t.Fatalf("Set-Cookie %q", c)
	}
	if r := b.do("GET", "/api/v1/me", nil); r.status != 200 || r.body["username"] != "admin" {
		t.Fatalf("me: %+v", r)
	}
	b.do("POST", "/api/v1/auth/logout", nil).expect(t, "logout", 204, "")
	b.do("GET", "/api/v1/me", nil).expect(t, "me after logout", 401, "unauthorized")

	for _, c := range [][2]string{{"admin", "wrong-password"}, {"ghost", "admin-password"}} {
		b.login(c[0], c[1]).expect(t, c[0]+"/"+c[1], 401, "invalid_credentials")
	}
	b.do("POST", "/api/v1/auth/login", nil).expect(t, "empty body", 400, "bad_request")
}

func TestLogoutDeletesSessionServerSide(t *testing.T) {
	e := newEnv(t)
	e.admin()
	b := e.browser()
	b.login("admin", "admin-password").expect(t, "login", 200, "")
	u, _ := http.NewRequest("GET", e.url, nil)
	cookie := b.c.Jar.Cookies(u.URL)[0]

	b.do("POST", "/api/v1/auth/logout", nil).expect(t, "logout", 204, "")
	// Тот же токен, сохранённый где-то ещё, тоже не действует.
	replay := e.browser()
	replay.c.Jar.SetCookies(u.URL, []*http.Cookie{cookie})
	replay.do("GET", "/api/v1/me", nil).expect(t, "replayed cookie", 401, "unauthorized")
}

func TestCrossOrigin(t *testing.T) {
	e := newEnv(t)
	e.admin()
	evil := e.browser()
	evil.header = map[string]string{"Sec-Fetch-Site": "cross-site"}
	evil.login("admin", "admin-password").expect(t, "cross-site login", 403, "cross_origin")
	// curl без Sec-Fetch-Site и Origin проходит.
	e.browser().login("admin", "admin-password").expect(t, "curl", 200, "")
}

func TestLoginRateLimitAndLog(t *testing.T) {
	e := newEnv(t)
	e.admin()
	b := e.browser()
	for i := range ratelimit.MaxFails {
		b.login("admin", fmt.Sprint("guess-", i)).expect(t, fmt.Sprint("guess ", i), 401, "invalid_credentials")
	}
	r := b.login("admin", "admin-password")
	r.expect(t, "blocked", 429, "too_many_attempts")
	if r.header.Get("Retry-After") != "900" {
		t.Fatalf("Retry-After %q", r.header.Get("Retry-After"))
	}
	log := e.logs.String()
	re := regexp.MustCompile(`"msg":"panel: authorization failed","user":"admin","ip":"127\.0\.0\.1","path":"/api/v1/auth/login"`)
	if n := len(re.FindAllString(log, -1)); n != ratelimit.MaxFails {
		t.Fatalf("%d failure lines, want %d:\n%s", n, ratelimit.MaxFails, log)
	}
	if strings.Contains(log, "guess-") {
		t.Fatal("password logged")
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	e.admin()
	first, second := e.browser(), e.browser()
	first.login("admin", "admin-password").expect(t, "first login", 200, "")
	second.login("admin", "admin-password").expect(t, "second login", 200, "")

	change := func(cur, next string) resp {
		return first.do("PUT", "/api/v1/me/password", map[string]string{"current_password": cur, "new_password": next})
	}
	change("admin-password", "short").expect(t, "short", 400, "invalid_password")
	change("wrong-password", "new-password").expect(t, "wrong current", 403, "invalid_credentials")
	change("admin-password", "new-password").expect(t, "change", 204, "")

	first.do("GET", "/api/v1/me", nil).expect(t, "current session", 200, "")
	second.do("GET", "/api/v1/me", nil).expect(t, "other session", 401, "unauthorized")
	e.browser().login("admin", "admin-password").expect(t, "old password", 401, "invalid_credentials")
	e.browser().login("admin", "new-password").expect(t, "new password", 200, "")
	if !strings.Contains(e.logs.String(), `"path":"/api/v1/me/password","reason"`) {
		t.Fatal("wrong current password not logged")
	}
}

func TestSetupFlow(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	if r := b.do("GET", "/api/v1/setup", nil); r.body["required"] != false {
		t.Fatalf("without code: %v", r.body)
	}
	code := e.setup.Generate()
	if r := b.do("GET", "/api/v1/setup", nil); r.body["required"] != true {
		t.Fatalf("with code: %v", r.body)
	}
	post := func(code, user, password string) resp {
		return b.do("POST", "/api/v1/setup", map[string]string{"code": code, "username": user, "password": password})
	}
	post("aaaa-bbbb-cccc", "admin", "admin-password").expect(t, "wrong code", 403, "invalid_setup_code")
	post(code, "admin", "short").expect(t, "short password", 400, "invalid_password")
	post(code, "a:b", "admin-password").expect(t, "bad name", 400, "invalid_username")
	r := post(strings.ToUpper(code), "admin", "admin-password")
	r.expect(t, "setup", 200, "")
	if r.body["role"] != "admin" {
		t.Fatalf("setup body %v", r.body)
	}
	b.do("GET", "/api/v1/me", nil).expect(t, "me after setup", 200, "")
	if r := b.do("GET", "/api/v1/setup", nil); r.body["required"] != false {
		t.Fatalf("after setup: %v", r.body)
	}
	post(code, "admin2", "admin-password").expect(t, "again", 409, "setup_completed")
	if !strings.Contains(e.logs.String(), `"msg":"panel: authorization failed","user":"admin","ip":"127.0.0.1","path":"/api/v1/setup"`) {
		t.Fatalf("wrong setup code not logged:\n%s", e.logs)
	}
}
