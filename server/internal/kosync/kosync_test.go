package kosync

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

const accept = "application/vnd.koreader.v1+json"

var now = time.Unix(1_700_000_000, 0)

type env struct {
	t      *testing.T
	ctx    context.Context
	path   string
	db     *store.DB
	acc    *accounts.Service
	h      *Handler
	keys   map[string]string // "user/device" → x-auth-key
	logBuf *strings.Builder
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
			pass, err := acc.AddDevice(ctx, u, d)
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
}

type resp struct {
	status int
	ctype  string
	body   any
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
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	out := resp{status: w.Code, ctype: w.Header().Get("Content-Type")}
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
