package kosync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

const accept = "application/vnd.koreader.v1+json"

var now = time.Unix(1_700_000_000, 0)

type env struct {
	t       *testing.T
	ctx     context.Context
	path    string
	db      *store.DB
	acc     *accounts.Service
	h       *Handler
	keys    map[string]string // "user/device" → x-auth-key
	logBuf  *strings.Builder
	guesses int
}

func newEnv(t *testing.T, opts Options) *env {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), store.FileName)
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := func() time.Time { return now }
	acc := accounts.New(db, accounts.WithClock(clock), accounts.WithArgon2Params(accounts.TestArgon2Params))
	logBuf := &strings.Builder{}
	h := New(acc, progress.New(db), opts, slog.New(slog.NewJSONHandler(logBuf, nil)))
	h.now = clock
	e := &env{t: t, ctx: ctx, path: path, db: db, acc: acc, h: h, keys: map[string]string{}, logBuf: logBuf}
	for _, u := range []string{"alice", "bob"} {
		if _, err := acc.CreateUser(ctx, u, accounts.RoleUser); err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{"Kindle", "Kobo"} {
			_, pass, err := acc.AddDevice(ctx, u, d)
			if err != nil {
				t.Fatal(err)
			}
			e.keys[u+"/"+d] = accounts.KeyFromPassword(pass)
		}
	}
	return e
}

type req struct {
	method, path, body string
	headers            map[string]string
	noAccept           bool
	as                 string // "user/device"
	remote             string // RemoteAddr, по умолчанию 192.0.2.1:1234
	xff                string
}

type resp struct {
	status     int
	ctype      string
	retryAfter string
	body       any
}

func (e *env) do(r req) resp {
	e.t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	if !r.noAccept {
		hr.Header.Set("Accept", accept)
	}
	if r.as != "" {
		user, _, _ := strings.Cut(r.as, "/")
		hr.Header.Set("x-auth-user", user)
		hr.Header.Set("x-auth-key", e.keys[r.as])
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	if r.remote != "" {
		hr.RemoteAddr = r.remote
	}
	if r.xff != "" {
		hr.Header.Set("X-Forwarded-For", r.xff)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	out := resp{status: w.Code, ctype: w.Header().Get("Content-Type"), retryAfter: w.Header().Get("Retry-After")}
	if w.Code != http.StatusNotFound {
		if err := json.Unmarshal(w.Body.Bytes(), &out.body); err != nil {
			e.t.Fatalf("%s %s: invalid JSON %q", r.method, r.path, w.Body.String())
		}
	}
	return out
}

func errBody(code int, msg string) map[string]any {
	return map[string]any{"code": float64(code), "message": msg}
}

func (e *env) check(name string, r req, status int, body any) {
	e.t.Helper()
	got := e.do(r)
	if got.status != status {
		e.t.Errorf("%s: status %d, want %d (body %v)", name, got.status, status, got.body)
		return
	}
	if status == http.StatusNotFound {
		return
	}
	if got.ctype != "application/json" {
		e.t.Errorf("%s: Content-Type %q", name, got.ctype)
	}
	if body != nil && !reflect.DeepEqual(got.body, body) {
		e.t.Errorf("%s: body %#v, want %#v", name, got.body, body)
	}
}

const goodPut = `{"document":"abc123","progress":"/body/DocFragment[3]","percentage":0.42,"device":"Kindle","device_id":"X1"}`

func TestFrameworkErrors(t *testing.T) {
	e := newEnv(t, Options{})
	tests := []struct {
		name   string
		r      req
		status int
		body   any
	}{
		{"no accept", req{method: "GET", path: "/healthcheck", noAccept: true}, 412, errBody(100, "Accept header not set.")},
		{"accept json", req{method: "GET", path: "/healthcheck", headers: map[string]string{"Accept": "application/json"}}, 412, errBody(101, "Invalid Accept header format.")},
		{"accept v2", req{method: "GET", path: "/healthcheck", headers: map[string]string{"Accept": "application/vnd.koreader.v2+json"}}, 412, errBody(102, "Unsupported version specified in the Accept header.")},
		{"plus is literal", req{method: "GET", path: "/healthcheck", headers: map[string]string{"Accept": "application/vnd.koreader.v1json"}}, 412, errBody(101, "Invalid Accept header format.")},
		{"accept suffix", req{method: "GET", path: "/healthcheck", headers: map[string]string{"Accept": "application/vnd.koreader.v1.beta+json"}}, 200, map[string]any{"state": "OK"}},
		{"accept v01", req{method: "GET", path: "/healthcheck", headers: map[string]string{"Accept": "application/vnd.koreader.v01+json"}}, 200, nil},
		{"dot is any char", req{method: "GET", path: "/healthcheck", headers: map[string]string{"Accept": "applicationXvndXkoreaderXv1+json"}}, 412, errBody(101, "Invalid Accept header format.")},
		{"dot is any char 2", req{method: "GET", path: "/healthcheck", headers: map[string]string{"Accept": "application/vndXkoreader.v1+json"}}, 200, nil},
		{"bad json no accept", req{method: "PUT", path: "/syncs/progress", body: "{", noAccept: true}, 400, errBody(103, "Could not parse JSON in body.")},
		{"bad json", req{method: "PUT", path: "/syncs/progress", body: "{"}, 400, errBody(103, "Could not parse JSON in body.")},
		{"bad json unknown path", req{method: "GET", path: "/users/nope", body: "nope"}, 400, errBody(103, "Could not parse JSON in body.")},
		{"array", req{method: "PUT", path: "/syncs/progress", body: "[1]", noAccept: true}, 400, errBody(104, "Body should be a JSON hash.")},
		{"array of null", req{method: "PUT", path: "/syncs/progress", body: "[null]"}, 400, errBody(104, "Body should be a JSON hash.")},
		{"unknown path", req{method: "GET", path: "/users/unknown"}, 404, nil},
		{"unknown path no accept", req{method: "GET", path: "/users/unknown", noAccept: true}, 412, errBody(100, "Accept header not set.")},
		{"wrong method", req{method: "GET", path: "/users/create"}, 404, nil},
		{"head is not get", req{method: "HEAD", path: "/healthcheck"}, 404, nil},
		{"healthcheck", req{method: "GET", path: "/healthcheck"}, 200, map[string]any{"state": "OK"}},
		{"healthcheck slash", req{method: "GET", path: "/healthcheck/"}, 200, map[string]any{"state": "OK"}},
		{"healthcheck escaped question", req{method: "GET", path: "/healthcheck/%3F"}, 200, nil},
	}
	for _, tt := range tests {
		e.check(tt.name, tt.r, tt.status, tt.body)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t, Options{})
	unauthorized := errBody(2001, "Unauthorized")
	e.check("ok", req{method: "GET", path: "/users/auth", as: "alice/Kindle"}, 200, map[string]any{"authorized": "OK"})
	e.check("second device", req{method: "GET", path: "/users/auth", as: "alice/Kobo"}, 200, map[string]any{"authorized": "OK"})
	e.check("no headers", req{method: "GET", path: "/users/auth"}, 401, unauthorized)
	e.check("wrong key", req{method: "GET", path: "/users/auth", headers: map[string]string{"x-auth-user": "alice", "x-auth-key": e.keys["bob/Kindle"]}}, 401, unauthorized)
	e.check("colon user", req{method: "GET", path: "/users/auth", headers: map[string]string{"x-auth-user": "a:b", "x-auth-key": "k"}}, 401, unauthorized)
	e.check("unknown user", req{method: "GET", path: "/users/auth", headers: map[string]string{"x-auth-user": "ghost", "x-auth-key": "k"}}, 401, unauthorized)

	if err := e.acc.RevokeDevice(e.ctx, "alice", "Kindle"); err != nil {
		t.Fatal(err)
	}
	e.check("revoked", req{method: "GET", path: "/users/auth", as: "alice/Kindle"}, 401, unauthorized)
	e.check("other still ok", req{method: "GET", path: "/users/auth", as: "alice/Kobo"}, 200, nil)
	if err := e.acc.DisableUser(e.ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	e.check("disabled", req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "alice/Kobo"}, 401, unauthorized)

	if !strings.Contains(e.logBuf.String(), `"user":"ghost"`) {
		t.Errorf("failed attempt not logged: %s", e.logBuf.String())
	}
	if strings.Contains(e.logBuf.String(), e.keys["bob/Kindle"]) {
		t.Error("key leaked to log")
	}

	long := strings.Repeat("я", 100_000) + ":"
	e.check("huge user", req{method: "GET", path: "/users/auth", headers: map[string]string{"x-auth-user": long, "x-auth-key": "k"}}, 401, unauthorized)
	last := e.logBuf.String()[strings.LastIndex(strings.TrimSuffix(e.logBuf.String(), "\n"), "\n")+1:]
	if len(last) > 512 || !strings.Contains(last, `"user":"`+strings.Repeat("я", 32)+`…"`) {
		t.Errorf("long user name not truncated in log (%d bytes)", len(last))
	}
}

func TestRegistration(t *testing.T) {
	off := newEnv(t, Options{})
	off.check("disabled", req{method: "POST", path: "/users/create", body: `{"username":"carol","password":"x"}`}, 402,
		errBody(2005, "User registration is disabled."))

	e := newEnv(t, Options{Registration: true})
	key := accounts.KeyFromPassword("pw")
	e.check("created", req{method: "POST", path: "/users/create", body: `{"username":"carol","password":"` + key + `"}`}, 201,
		map[string]any{"username": "carol"})
	e.check("auth new", req{method: "GET", path: "/users/auth", headers: map[string]string{"x-auth-user": "carol", "x-auth-key": key}}, 200, nil)
	e.check("taken", req{method: "POST", path: "/users/create", body: `{"username":"alice","password":"x"}`}, 402,
		errBody(2002, "Username is already registered."))
	invalid := errBody(2003, "Invalid request")
	for _, b := range []string{`{"username":"","password":"x"}`, `{"username":"a:b","password":"x"}`, `{"username":"dave","password":""}`, `{"username":"dave"}`, `{"username":5,"password":"x"}`, ``} {
		e.check("invalid "+b, req{method: "POST", path: "/users/create", body: b}, 403, invalid)
	}
}

func TestProgress(t *testing.T) {
	e := newEnv(t, Options{})
	missing := errBody(2004, "Field 'document' not provided.")
	invalid := errBody(2003, "Invalid request")
	unservable := errBody(2007, "Field 'document' contains characters the read route cannot serve.")

	e.check("empty get", req{method: "GET", path: "/syncs/progress/abc123", as: "alice/Kindle"}, 200, map[string]any{})
	e.check("put", req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "alice/Kindle"}, 200,
		map[string]any{"document": "abc123", "timestamp": float64(now.Unix())})
	want := map[string]any{"document": "abc123", "percentage": 0.42, "progress": "/body/DocFragment[3]",
		"device": "Kindle", "device_id": "X1", "timestamp": float64(now.Unix())}
	e.check("get", req{method: "GET", path: "/syncs/progress/abc123", as: "alice/Kobo"}, 200, want)
	e.check("get trailing slash", req{method: "GET", path: "/syncs/progress/abc123/", as: "alice/Kobo"}, 200, want)
	e.check("isolation", req{method: "GET", path: "/syncs/progress/abc123", as: "bob/Kindle"}, 200, map[string]any{})
	e.check("get unauthorized", req{method: "GET", path: "/syncs/progress/abc123"}, 401, errBody(2001, "Unauthorized"))
	e.check("get dash", req{method: "GET", path: "/syncs/progress/ab-c", as: "alice/Kindle"}, 404, nil)
	e.check("get colon", req{method: "GET", path: "/syncs/progress/a:b", as: "alice/Kindle"}, 404, nil)
	e.check("get nested", req{method: "GET", path: "/syncs/progress/abc/def", as: "alice/Kindle"}, 404, nil)

	e.check("put unauthorized", req{method: "PUT", path: "/syncs/progress", body: `{}`}, 401, errBody(2001, "Unauthorized"))
	for _, b := range []string{``, `null`, `5`, `"str"`, `[]`, `{}`, `{"document":""}`, `{"document":"a:b"}`, `{"document":5}`} {
		e.check("missing doc "+b, req{method: "PUT", path: "/syncs/progress", body: b, as: "alice/Kindle"}, 403, missing)
	}
	e.check("unservable", req{method: "PUT", path: "/syncs/progress", body: `{"document":"ab-c","progress":"p","percentage":0.1,"device":"d"}`, as: "alice/Kindle"}, 403, unservable)
	for _, b := range []string{
		`{"document":"d1","percentage":0.1,"device":"d"}`,
		`{"document":"d1","progress":"p","device":"d"}`,
		`{"document":"d1","progress":"p","percentage":0.1}`,
		`{"document":"d1","progress":"p","percentage":"abc","device":"d"}`,
		`{"document":"d1","progress":"p","percentage":true,"device":"d"}`,
		`{"document":"d1","progress":false,"percentage":0.1,"device":"d"}`,
	} {
		e.check("invalid "+b, req{method: "PUT", path: "/syncs/progress", body: b, as: "alice/Kindle"}, 403, invalid)
	}

	e.check("percentage string", req{method: "PUT", path: "/syncs/progress", body: `{"document":"d2","progress":"5","percentage":" 0.5 ","device":"Kobo"}`, as: "alice/Kobo"}, 200, nil)
	e.check("percentage string get", req{method: "GET", path: "/syncs/progress/d2", as: "alice/Kobo"}, 200,
		map[string]any{"document": "d2", "percentage": 0.5, "progress": "5", "device": "Kobo", "timestamp": float64(now.Unix())})

	// Без device_id остаётся прежний, как в хеше Redis эталона.
	e.check("put without device_id", req{method: "PUT", path: "/syncs/progress", body: `{"document":"abc123","progress":"p2","percentage":0.5,"device":"Kobo"}`, as: "alice/Kobo"}, 200, nil)
	e.check("device_id kept", req{method: "GET", path: "/syncs/progress/abc123", as: "alice/Kobo"}, 200,
		map[string]any{"document": "abc123", "percentage": 0.5, "progress": "p2", "device": "Kobo", "device_id": "X1", "timestamp": float64(now.Unix())})

	// Не-строковые progress/device приводятся к строке, как tostring в Lua.
	e.check("numeric progress", req{method: "PUT", path: "/syncs/progress", body: `{"document":"d3","progress":12,"percentage":1,"device":"K","device_id":7}`, as: "alice/Kobo"}, 200, nil)
	e.check("numeric progress get", req{method: "GET", path: "/syncs/progress/d3", as: "alice/Kobo"}, 200,
		map[string]any{"document": "d3", "percentage": float64(1), "progress": "12", "device": "K", "device_id": "7", "timestamp": float64(now.Unix())})
}

func TestRevokedBetweenCheckAndWrite(t *testing.T) {
	e := newEnv(t, Options{})
	cli, err := store.Open(e.ctx, e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	cliAcc := accounts.New(cli, accounts.WithArgon2Params(accounts.TestArgon2Params))
	e.h.beforeWrite = func() {
		if err := cliAcc.RevokeDevice(e.ctx, "alice", "Kindle"); err != nil {
			t.Fatal(err)
		}
	}
	e.check("revoked mid-request", req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "alice/Kindle"}, 401, errBody(2001, "Unauthorized"))
	e.h.beforeWrite = nil
	var events, docs, current int
	e.db.Read.QueryRow(`SELECT count(*) FROM progress_events`).Scan(&events)
	e.db.Read.QueryRow(`SELECT count(*) FROM documents`).Scan(&docs)
	e.db.Read.QueryRow(`SELECT count(*) FROM progress_current`).Scan(&current)
	if events+docs+current != 0 {
		t.Fatalf("written: events=%d documents=%d current=%d", events, docs, current)
	}
}

func TestPasswordRotation(t *testing.T) {
	e := newEnv(t, Options{})
	newKey := accounts.KeyFromPassword("new")
	e.check("empty password", req{method: "PUT", path: "/users/password", body: `{"password":""}`, as: "alice/Kindle"}, 403, errBody(2003, "Invalid request"))
	// Тело проверяется раньше ключа, как в эталоне.
	e.check("empty password bad key", req{method: "PUT", path: "/users/password", body: `{}`, headers: map[string]string{"x-auth-user": "alice", "x-auth-key": "bad"}}, 403, errBody(2003, "Invalid request"))
	e.check("no headers", req{method: "PUT", path: "/users/password", body: `{}`}, 401, errBody(2001, "Unauthorized"))
	e.check("wrong key", req{method: "PUT", path: "/users/password", body: `{"password":"x"}`, headers: map[string]string{"x-auth-user": "alice", "x-auth-key": "bad"}}, 401, errBody(2001, "Unauthorized"))
	e.check("rotate", req{method: "PUT", path: "/users/password", body: `{"password":"` + newKey + `"}`, as: "alice/Kindle"}, 200, map[string]any{"updated": true})
	e.check("old key", req{method: "GET", path: "/users/auth", as: "alice/Kindle"}, 401, nil)
	e.check("new key", req{method: "GET", path: "/users/auth", headers: map[string]string{"x-auth-user": "alice", "x-auth-key": newKey}}, 200, nil)
	e.check("other device", req{method: "GET", path: "/users/auth", as: "alice/Kobo"}, 200, nil)
}

func TestDeleteMe(t *testing.T) {
	off := newEnv(t, Options{})
	off.check("disabled", req{method: "DELETE", path: "/users/me", as: "alice/Kindle"}, 403, errBody(2003, "Invalid request"))
	off.check("still there", req{method: "GET", path: "/users/auth", as: "alice/Kindle"}, 200, nil)

	e := newEnv(t, Options{SelfDelete: true})
	e.check("put", req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "alice/Kindle"}, 200, nil)
	e.check("put bob", req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "bob/Kindle"}, 200, nil)
	e.check("no headers", req{method: "DELETE", path: "/users/me"}, 401, errBody(2001, "Unauthorized"))
	e.check("wrong key", req{method: "DELETE", path: "/users/me", headers: map[string]string{"x-auth-user": "alice", "x-auth-key": "bad"}}, 401, errBody(2001, "Unauthorized"))
	e.check("unknown", req{method: "DELETE", path: "/users/me", headers: map[string]string{"x-auth-user": "ghost", "x-auth-key": "bad"}}, 404, errBody(2006, "Account not found."))
	e.check("delete", req{method: "DELETE", path: "/users/me", as: "alice/Kindle"}, 200, map[string]any{"deleted": true})
	e.check("gone", req{method: "GET", path: "/users/auth", as: "alice/Kobo"}, 401, nil)
	e.check("bob kept", req{method: "GET", path: "/syncs/progress/abc123", as: "bob/Kindle"}, 200, nil)
	var docs int
	e.db.Read.QueryRow(`SELECT count(*) FROM documents`).Scan(&docs)
	if docs != 1 {
		t.Fatalf("documents = %d, want 1", docs)
	}
}

// limited пересобирает обработчик с ограничителем на тех же часах.
func (e *env) limited(opts Options) *env {
	log := slog.New(slog.NewJSONHandler(e.logBuf, nil))
	clock := func() time.Time { return now }
	lim := ratelimit.New(log, ratelimit.WithClock(clock))
	e.acc = accounts.New(e.db, accounts.WithClock(clock), accounts.WithArgon2Params(accounts.TestArgon2Params), accounts.WithLimiter(lim))
	e.h = New(e.acc, progress.New(e.db), opts, log)
	e.h.now = clock
	return e
}

// fail шлёт n запросов GET /users/auth с разными неверными ключами.
func (e *env) fail(n int, r req) {
	e.t.Helper()
	for range n {
		e.guesses++
		r.method, r.path = "GET", "/users/auth"
		r.headers = map[string]string{"x-auth-user": "alice", "x-auth-key": fmt.Sprint("guess", e.guesses)}
		e.check(fmt.Sprint("guess ", e.guesses), r, 401, errBody(2001, "Unauthorized"))
	}
}

func (e *env) checkBlocked(name string, r req, status int, body any) {
	e.t.Helper()
	got := e.do(r)
	if got.status != status || got.ctype != "application/json" || got.retryAfter != "900" || !reflect.DeepEqual(got.body, body) {
		e.t.Errorf("%s: %d %q Retry-After=%q %#v", name, got.status, got.ctype, got.retryAfter, got.body)
	}
}

func TestRateLimit(t *testing.T) {
	opts := Options{SelfDelete: true}
	e := newEnv(t, opts).limited(opts)
	blocked := errBody(2001, "Too many failed attempts. Try again in 15 minutes.")
	e.check("cache kindle", req{method: "GET", path: "/users/auth", as: "alice/Kindle"}, 200, nil)
	e.fail(ratelimit.MaxFails, req{})

	e.checkBlocked("auth", req{method: "GET", path: "/users/auth", headers: map[string]string{"x-auth-user": "alice", "x-auth-key": "new guess"}}, 401, blocked)
	e.checkBlocked("auth valid uncached", req{method: "GET", path: "/users/auth", as: "alice/Kobo"}, 401, blocked)
	e.checkBlocked("put progress", req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "alice/Kobo"}, 401, blocked)
	e.checkBlocked("get progress", req{method: "GET", path: "/syncs/progress/abc123", as: "bob/Kindle"}, 401, blocked)
	e.checkBlocked("password", req{method: "PUT", path: "/users/password", body: `{"password":"x"}`, as: "bob/Kindle"}, 401, blocked)
	e.checkBlocked("delete me", req{method: "DELETE", path: "/users/me", as: "bob/Kobo"}, 401, blocked)
	e.checkBlocked("delete unknown", req{method: "DELETE", path: "/users/me", headers: map[string]string{"x-auth-user": "ghost", "x-auth-key": "k"}}, 401, blocked)

	// Ошибки до проверки ключа не меняются.
	e.check("bad json", req{method: "PUT", path: "/syncs/progress", body: "{", as: "alice/Kobo"}, 400, errBody(103, "Could not parse JSON in body."))
	e.check("array", req{method: "PUT", path: "/syncs/progress", body: "[1]", as: "alice/Kobo"}, 400, errBody(104, "Body should be a JSON hash."))
	e.check("no accept", req{method: "GET", path: "/users/auth", as: "alice/Kobo", noAccept: true}, 412, errBody(100, "Accept header not set."))
	e.check("no headers", req{method: "GET", path: "/users/auth"}, 401, errBody(2001, "Unauthorized"))
	e.check("password body first", req{method: "PUT", path: "/users/password", body: `{}`, as: "bob/Kindle"}, 403, errBody(2003, "Invalid request"))

	// Закешированное устройство и другой адрес обслуживаются.
	e.check("cached kindle", req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "alice/Kindle"}, 200, nil)
	e.check("other address", req{method: "GET", path: "/users/auth", as: "alice/Kobo", remote: "198.51.100.1:1"}, 200, nil)

	// Неудачи до блокировки и запрос без заголовков; отказы из-за блокировки не логируются.
	if n := strings.Count(e.logBuf.String(), "authorization failed"); n != ratelimit.MaxFails+1 {
		t.Errorf("%d failures logged, want %d", n, ratelimit.MaxFails+1)
	}
	if n := strings.Count(e.logBuf.String(), "auth: client blocked"); n != 1 {
		t.Errorf("%d block warnings", n)
	}
}

func TestRateLimitRevokedReader(t *testing.T) {
	e := newEnv(t, Options{}).limited(Options{})
	if err := e.acc.RevokeDevice(e.ctx, "alice", "Kindle"); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		e.check(fmt.Sprint("revoked ", i), req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "alice/Kindle"}, 401, errBody(2001, "Unauthorized"))
	}
	e.check("kobo", req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "alice/Kobo"}, 200, nil)
	if n := strings.Count(e.logBuf.String(), "authorization failed"); n != 1 {
		t.Errorf("repeated rejection logged %d times, want once", n)
	}
}

func TestRateLimitRegistration(t *testing.T) {
	off := newEnv(t, Options{}).limited(Options{})
	for i := range 2 * ratelimit.MaxFails {
		off.check(fmt.Sprint("disabled ", i), req{method: "POST", path: "/users/create", body: `{"username":"carol","password":"x"}`}, 402,
			errBody(2005, "User registration is disabled."))
	}
	off.check("budget intact", req{method: "GET", path: "/users/auth", headers: map[string]string{"x-auth-user": "alice", "x-auth-key": "k"}}, 401, errBody(2001, "Unauthorized"))

	opts := Options{Registration: true}
	e := newEnv(t, opts).limited(opts)
	for i := range 2 * ratelimit.MaxFails {
		e.check(fmt.Sprint("invalid ", i), req{method: "POST", path: "/users/create", body: `{"username":"a:b","password":"x"}`}, 403, errBody(2003, "Invalid request"))
	}
	for i := range ratelimit.MaxFails - 1 {
		e.check(fmt.Sprint("create ", i), req{method: "POST", path: "/users/create", body: fmt.Sprintf(`{"username":"user%d","password":"x"}`, i)}, 201, nil)
	}
	e.check("taken counts too", req{method: "POST", path: "/users/create", body: `{"username":"alice","password":"x"}`}, 402, errBody(2002, "Username is already registered."))
	e.checkBlocked("blocked", req{method: "POST", path: "/users/create", body: `{"username":"zed","password":"x"}`}, 402,
		errBody(2005, "Too many registration attempts. Try again in 15 minutes."))
}

func TestClientAddress(t *testing.T) {
	trusted := Options{ClientIP: clientip.New([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})}
	e := newEnv(t, trusted).limited(trusted)
	viaProxy := req{remote: "10.0.0.2:5000", xff: "1.1.1.1, 198.51.100.7"}
	e.fail(1, viaProxy)
	if !strings.Contains(e.logBuf.String(), `"ip":"198.51.100.7"`) {
		t.Fatalf("client address not logged: %s", e.logBuf)
	}
	e.fail(ratelimit.MaxFails-1, viaProxy)
	e.checkBlocked("blocked via proxy", req{method: "GET", path: "/users/auth", as: "alice/Kobo", remote: "10.0.0.2:5000", xff: "198.51.100.7"}, 401,
		errBody(2001, "Too many failed attempts. Try again in 15 minutes."))
	e.check("other client via same proxy", req{method: "GET", path: "/users/auth", as: "alice/Kobo", remote: "10.0.0.2:5000", xff: "198.51.100.8"}, 200, nil)

	// Без доверенных прокси заголовок игнорируется.
	plain := newEnv(t, Options{})
	plain.fail(1, req{remote: "203.0.113.5:4000", xff: "198.51.100.7"})
	if !strings.Contains(plain.logBuf.String(), `"ip":"203.0.113.5"`) {
		t.Fatalf("connection address not logged: %s", plain.logBuf)
	}
}
