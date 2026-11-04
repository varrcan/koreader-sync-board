package panelauth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

var client = netip.MustParseAddr("192.0.2.1")

type env struct {
	ctx     context.Context
	now     time.Time
	acc     *accounts.Service
	limiter *ratelimit.Limiter
	auth    *Auth
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{ctx: ctx, now: time.Unix(1_700_000_000, 0)}
	clock := func() time.Time { return e.now }
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	e.limiter = ratelimit.New(log, ratelimit.WithClock(clock))
	e.acc = accounts.New(db, accounts.WithClock(clock), accounts.WithArgon2Params(accounts.TestArgon2Params), accounts.WithLimiter(e.limiter))
	e.auth = New(e.acc, clientip.New(nil), log)
	return e
}

func (e *env) session(t *testing.T) string {
	t.Helper()
	u, err := e.acc.CreateFirstAdmin(e.ctx, "admin", "admin-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := e.acc.CreateSession(e.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// serve прогоняет запрос через ServeMux, как сгенерированный маршрутизатор:
// middleware видит заполненный r.Pattern.
func (e *env) serve(r *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	h := e.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, ok := FromContext(r.Context()); ok {
			io.WriteString(w, c.User.Username)
		}
	}))
	mux.Handle("GET /api/v1/health", h)
	mux.Handle("GET /api/v1/me", h)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func withCookie(r *http.Request, token string) *http.Request {
	r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	return r
}

func TestMiddleware(t *testing.T) {
	e := newEnv(t)
	token := e.session(t)

	if w := e.serve(httptest.NewRequest("GET", "/api/v1/health", nil)); w.Code != 200 {
		t.Fatalf("public without session: %d", w.Code)
	}
	for name, r := range map[string]*http.Request{
		"no cookie":  httptest.NewRequest("GET", "/api/v1/me", nil),
		"bad cookie": withCookie(httptest.NewRequest("GET", "/api/v1/me", nil), "bogus"),
	} {
		w := e.serve(r)
		if w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"unauthorized"`) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	w := e.serve(withCookie(httptest.NewRequest("GET", "/api/v1/me", nil), token))
	if w.Code != 200 || w.Body.String() != "admin" {
		t.Fatalf("with session: %d %s", w.Code, w.Body)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("cookie re-set without renewal")
	}
}

func TestCookie(t *testing.T) {
	e := newEnv(t)
	token := e.session(t)
	e.now = e.now.Add(2 * time.Minute) // продление: cookie выставляется заново

	for _, c := range []struct {
		name   string
		mod    func(*http.Request)
		secure bool
	}{
		{"plain http", func(*http.Request) {}, false},
		{"tls", func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, true},
		{"proxy https", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, true},
		{"proxy http", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") }, false},
	} {
		r := withCookie(httptest.NewRequest("GET", "/api/v1/me", nil), token)
		c.mod(r)
		e.now = e.now.Add(2 * time.Minute)
		w := e.serve(r)
		cookies := w.Result().Cookies()
		if w.Code != 200 || len(cookies) != 1 {
			t.Fatalf("%s: %d, cookies %v", c.name, w.Code, cookies)
		}
		ck := cookies[0]
		if ck.Name != CookieName || ck.Value != token || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode ||
			ck.Path != "/" || ck.MaxAge != int(accounts.SessionTTL/time.Second) || ck.Secure != c.secure {
			t.Fatalf("%s: cookie %+v", c.name, ck)
		}
	}

	w := httptest.NewRecorder()
	ClearCookie(w, httptest.NewRequest("POST", "/api/v1/auth/logout", nil))
	if ck := w.Result().Cookies(); len(ck) != 1 || ck[0].MaxAge != -1 || ck[0].Value != "" {
		t.Fatalf("cleared cookie: %+v", ck)
	}
}

func TestCrossOrigin(t *testing.T) {
	h := CrossOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, c := range []struct {
		name    string
		method  string
		headers map[string]string
		status  int
	}{
		{"cross-site post", "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"foreign origin", "POST", map[string]string{"Origin": "https://evil.example"}, 403},
		{"same-origin post", "POST", map[string]string{"Sec-Fetch-Site": "same-origin"}, 204},
		{"curl", "POST", nil, 204},
		{"cross-site get", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, 204},
	} {
		r := httptest.NewRequest(c.method, "http://ksb.lan/api/v1/auth/login", nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.status {
			t.Fatalf("%s: %d", c.name, w.Code)
		}
		if c.status == 403 && !strings.Contains(w.Body.String(), `"code":"cross_origin"`) {
			t.Fatalf("%s: body %s", c.name, w.Body)
		}
	}
}

func TestSetupCode(t *testing.T) {
	e := newEnv(t)
	s := NewSetup(e.acc, e.limiter)
	if ok, err := s.Required(e.ctx); ok || err != nil {
		t.Fatalf("required without code: %v %v", ok, err)
	}
	code := s.Generate()
	if !regexp.MustCompile(`^[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}$`).MatchString(code) {
		t.Fatalf("code %q", code)
	}
	if ok, _ := s.Required(e.ctx); !ok {
		t.Fatal("not required with code and no users")
	}

	// Неверный код тратит бюджет.
	if _, err := s.Complete(e.ctx, "aaaa-bbbb-cccc", "admin", "admin-password", client); !errors.Is(err, ErrInvalidSetupCode) {
		t.Fatalf("wrong code: %v", err)
	}
	// Ошибка ввода при верном коде не тратит ни бюджет, ни код.
	for range ratelimit.MaxFails {
		if _, err := s.Complete(e.ctx, code, "admin", "short", client); !errors.Is(err, accounts.ErrInvalidPassword) {
			t.Fatalf("short password: %v", err)
		}
		if _, err := s.Complete(e.ctx, code, "a:b", "admin-password", client); !errors.Is(err, accounts.ErrInvalidUsername) {
			t.Fatalf("bad name: %v", err)
		}
	}
	if blocked, _ := e.limiter.Blocked(client); blocked {
		t.Fatal("input errors spent the budget")
	}
	// Регистр, пробелы и дефисы не важны.
	messy := "  " + strings.ToUpper(strings.ReplaceAll(code, "-", "")) + " "
	u, err := s.Complete(e.ctx, messy, "admin", "admin-password", client)
	if err != nil || u.Role != accounts.RoleAdmin {
		t.Fatalf("complete: %+v %v", u, err)
	}
	if ok, _ := s.Required(e.ctx); ok {
		t.Fatal("still required after setup")
	}
	if _, err := s.Complete(e.ctx, code, "admin2", "admin-password", client); !errors.Is(err, accounts.ErrSetupCompleted) {
		t.Fatalf("second setup: %v", err)
	}
}

func TestSetupCodeErasedAfterSuccess(t *testing.T) {
	e := newEnv(t)
	s := NewSetup(e.acc, e.limiter)
	code := s.Generate()
	if _, err := s.Complete(e.ctx, code, "admin", "admin-password", client); err != nil {
		t.Fatal(err)
	}
	// Все пользователи удалены из CLI: старый код уже не подходит.
	if err := e.acc.DeleteUser(e.ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Required(e.ctx); ok {
		t.Fatal("required with erased code")
	}
	if _, err := s.Complete(e.ctx, code, "admin", "admin-password", client); !errors.Is(err, ErrInvalidSetupCode) {
		t.Fatalf("reused code: %v", err)
	}
}

func TestSetupUserFromCLI(t *testing.T) {
	e := newEnv(t)
	s := NewSetup(e.acc, e.limiter)
	code := s.Generate()
	if _, err := e.acc.CreateUser(e.ctx, "alice", accounts.RoleUser); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Required(e.ctx); ok {
		t.Fatal("required with a user from CLI")
	}
	// 409 без проверки кода: бюджет не тратится и неверным кодом.
	for i := range ratelimit.MaxFails + 1 {
		if _, err := s.Complete(e.ctx, fmt.Sprint("bad", i), "admin", "admin-password", client); !errors.Is(err, accounts.ErrSetupCompleted) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := s.Complete(e.ctx, code, "admin", "admin-password", client); !errors.Is(err, accounts.ErrSetupCompleted) {
		t.Fatal(err)
	}
}

func TestSetupRateLimit(t *testing.T) {
	e := newEnv(t)
	s := NewSetup(e.acc, e.limiter)
	code := s.Generate()
	for i := range ratelimit.MaxFails {
		if _, err := s.Complete(e.ctx, fmt.Sprint("guess", i), "admin", "admin-password", client); !errors.Is(err, ErrInvalidSetupCode) {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	var rl *accounts.RateLimitedError
	if _, err := s.Complete(e.ctx, code, "admin", "admin-password", client); !errors.As(err, &rl) {
		t.Fatalf("blocked: %v", err)
	}
}
