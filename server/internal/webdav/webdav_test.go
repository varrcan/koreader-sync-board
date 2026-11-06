package webdav

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics/statstest"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

// propfindBody — тело PROPFIND из WebDavApi.listFolder (providers/webdav.lua).
const propfindBody = `<?xml version="1.0"?><a:propfind xmlns:a="DAV:"><a:prop><a:resourcetype/><a:getcontentlength/><a:getlastmodified/></a:prop></a:propfind>`

type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	t     *testing.T
	ctx   context.Context
	db    *store.DB
	acc   *accounts.Service
	stats *statistics.Store
	h     *Handler
	pass  map[string]string // "user/device" → пароль
	logs  *logBuffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logs := &logBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	acc := accounts.New(db, accounts.WithArgon2Params(accounts.TestArgon2Params), accounts.WithLimiter(ratelimit.New(log)))
	stats := statistics.New(db, t.TempDir(), log)
	e := &env{t: t, ctx: ctx, db: db, acc: acc, stats: stats, h: New(acc, stats, clientip.New(nil), log),
		pass: map[string]string{}, logs: logs}
	for _, u := range []string{"alice", "bob"} {
		if _, err := acc.CreateUser(ctx, u, accounts.RoleUser); err != nil {
			t.Fatal(err)
		}
		_, pass, err := acc.AddDevice(ctx, u, "Kobo")
		if err != nil {
			t.Fatal(err)
		}
		e.pass[u+"/Kobo"] = pass
	}
	return e
}

type req struct {
	method, path string
	body         []byte
	as           string // "user/device"; пусто — без Authorization
	headers      map[string]string
}

func (e *env) do(r req) *httptest.ResponseRecorder {
	e.t.Helper()
	hr := httptest.NewRequest(r.method, r.path, bytes.NewReader(r.body))
	if r.as != "" {
		user, _, _ := strings.Cut(r.as, "/")
		hr.SetBasicAuth(user, e.pass[r.as])
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	return w
}

func (e *env) put(as, name string, body []byte, ifMatch string) *httptest.ResponseRecorder {
	e.t.Helper()
	h := map[string]string{}
	if ifMatch != "" {
		h["If-Match"] = ifMatch
	}
	return e.do(req{method: "PUT", path: "/dav/" + name, body: body, as: as, headers: h})
}

func stats(t *testing.T, titles ...string) []byte {
	var books []statstest.Book
	for i, title := range titles {
		books = append(books, statstest.Book{ID: int64(i + 1), Title: title, Authors: "N/A", MD5: fmt.Sprintf("%032x", i),
			Pages: 10, Records: []statstest.Record{{Page: 1, StartTime: 1000, Duration: 30, TotalPages: 10}}})
	}
	return statstest.Build(t, books)
}

func expect(t *testing.T, name string, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("%s: status %d, want %d (%s)", name, w.Code, status, w.Body.String())
	}
}

// Шаблоны Lua из WebDavApi.listFolder: «.-» — ленивое «.*», «%s» и «%d» — классы,
// «.» в Lua совпадает и с переводом строки.
var (
	luaResponse      = regexp.MustCompile(`(?s)<[^>]*response[^>]*>(.*?)</[^>]*response>`)
	luaHref          = regexp.MustCompile(`(?s)<[^>]*href[^>]*>(.*)</[^>]*href>`)
	luaNotCollection = []*regexp.Regexp{
		regexp.MustCompile(`<[^>]*resourcetype\s*/>`),
		regexp.MustCompile(`<[^>]*resourcetype>\s*</[^>]*resourcetype>`),
	}
	luaCollection = []*regexp.Regexp{
		regexp.MustCompile(`<[^>]*collection[^<]*/>`),
		regexp.MustCompile(`<[^>]*collection>\s*</[^>]*collection>`),
	}
	luaLength   = regexp.MustCompile(`<[^>]*getcontentlength[^>]*>(\d+)</[^>]*getcontentlength>`)
	luaModified = regexp.MustCompile(`(?s)<[^>]*getlastmodified[^>]*>(.*)</[^>]*getlastmodified>`)
	etagRe      = regexp.MustCompile(`<[^>]*getetag[^>]*>([^<]*)</[^>]*getetag>`)
)

// item — элемент листинга так, как его видит KOReader.
type item struct {
	href     string
	folder   bool
	size     string
	modified string
	etag     string
}

func anyMatch(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func listFolder(t *testing.T, body string) []item {
	t.Helper()
	var out []item
	for _, m := range luaResponse.FindAllStringSubmatch(body, -1) {
		it := m[1]
		var i item
		if h := luaHref.FindStringSubmatch(it); h != nil {
			i.href = h[1]
		}
		switch {
		case anyMatch(luaNotCollection, it):
			if l := luaLength.FindStringSubmatch(it); l != nil {
				i.size = l[1]
			}
			if l := luaModified.FindStringSubmatch(it); l != nil {
				i.modified = l[1]
			}
			if l := etagRe.FindStringSubmatch(it); l != nil {
				i.etag = strings.ReplaceAll(l[1], "&#34;", `"`)
			}
		case anyMatch(luaCollection, it):
			i.folder = true
		default:
			t.Fatalf("item is neither file nor folder: %s", it)
		}
		out = append(out, i)
	}
	return out
}

func TestPropfind(t *testing.T) {
	e := newEnv(t)
	propfind := func(path, depth string) []item {
		t.Helper()
		w := e.do(req{method: "PROPFIND", path: path, body: []byte(propfindBody), as: "alice/Kobo",
			headers: map[string]string{"Depth": depth, "Content-Type": "application/xml"}})
		expect(t, "PROPFIND "+path, w, http.StatusMultiStatus)
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
			t.Errorf("Content-Type %q", ct)
		}
		return listFolder(t, w.Body.String())
	}

	if got := propfind("/dav/", "1"); len(got) != 1 || !got[0].folder || got[0].href != "/dav/" {
		t.Fatalf("empty folder: %+v", got)
	}
	data := stats(t, "A")
	put := e.put("alice/Kobo", statistics.FileStatistics, data, "")
	expect(t, "PUT", put, http.StatusCreated)

	for _, depth := range []string{"1", "infinity", ""} {
		got := propfind("/dav/", depth)
		if len(got) != 2 || !got[0].folder || got[1].folder {
			t.Fatalf("depth %q: %+v", depth, got)
		}
		f := got[1]
		if f.href != "/dav/statistics.sqlite3" || f.size != strconv.Itoa(len(data)) || f.etag != put.Header().Get("ETag") {
			t.Errorf("depth %q: file %+v", depth, f)
		}
		if _, err := time.Parse(http.TimeFormat, f.modified); err != nil {
			t.Errorf("getlastmodified %q: %v", f.modified, err)
		}
	}
	if got := propfind("/dav/", "0"); len(got) != 1 || !got[0].folder {
		t.Fatalf("depth 0: %+v", got)
	}
	if got := propfind("/dav", "1"); len(got) != 2 || got[0].href != "/dav" || got[1].href != "/dav/statistics.sqlite3" {
		t.Fatalf("without slash: %+v", got)
	}
	if got := propfind("/dav/statistics.sqlite3", "0"); len(got) != 1 || got[0].folder || got[0].size != strconv.Itoa(len(data)) {
		t.Fatalf("file: %+v", got)
	}
	for _, path := range []string{"/dav/vocabulary_builder.sqlite3", "/dav/books.epub", "/dav/sub/"} {
		expect(t, "PROPFIND "+path, e.do(req{method: "PROPFIND", path: path, as: "alice/Kobo"}), http.StatusNotFound)
	}
	// Файлы bob — в его пространстве.
	w := e.do(req{method: "PROPFIND", path: "/dav/", as: "bob/Kobo", headers: map[string]string{"Depth": "1"}})
	if got := listFolder(t, w.Body.String()); len(got) != 1 {
		t.Fatalf("bob sees %+v", got)
	}
}

func TestGetHead(t *testing.T) {
	e := newEnv(t)
	get := func(method, as string) *httptest.ResponseRecorder {
		return e.do(req{method: method, path: "/dav/statistics.sqlite3", as: as})
	}
	expect(t, "GET before upload", get("GET", "alice/Kobo"), http.StatusNotFound)

	data := stats(t, "A", "B")
	put := e.put("alice/Kobo", statistics.FileStatistics, data, "")
	expect(t, "PUT", put, http.StatusCreated)
	etag := put.Header().Get("ETag")
	if !regexp.MustCompile(`^"[0-9a-f]{32}"$`).MatchString(etag) {
		t.Fatalf("ETag %q", etag)
	}

	for _, method := range []string{"GET", "HEAD"} {
		w := get(method, "alice/Kobo")
		expect(t, method, w, http.StatusOK)
		if w.Header().Get("ETag") != etag || w.Header().Get("Content-Type") != "application/octet-stream" ||
			w.Header().Get("Last-Modified") == "" || w.Header().Get("Content-Length") != strconv.Itoa(len(data)) {
			t.Errorf("%s headers: %v", method, w.Header())
		}
		if method == "GET" && !bytes.Equal(w.Body.Bytes(), data) {
			t.Error("GET body differs from uploaded file")
		}
	}
	w := e.do(req{method: "GET", path: "/dav/statistics.sqlite3", as: "alice/Kobo", headers: map[string]string{"If-None-Match": etag}})
	expect(t, "If-None-Match", w, http.StatusNotModified)
	if w.Header().Get("ETag") != etag {
		t.Error("304 without ETag")
	}
	w = e.do(req{method: "GET", path: "/dav/statistics.sqlite3", as: "alice/Kobo", headers: map[string]string{"Range": "bytes=0-15"}})
	expect(t, "Range", w, http.StatusPartialContent)
	if w.Body.String() != "SQLite format 3\x00" {
		t.Errorf("range body %q", w.Body.String())
	}

	expect(t, "bob", get("GET", "bob/Kobo"), http.StatusNotFound)
	expect(t, "unknown name", e.do(req{method: "GET", path: "/dav/books.epub", as: "alice/Kobo"}), http.StatusNotFound)
	expect(t, "GET root", e.do(req{method: "GET", path: "/dav/", as: "alice/Kobo"}), http.StatusMethodNotAllowed)
}

func TestPut(t *testing.T) {
	e := newEnv(t)
	v1, v2, v3 := stats(t, "A"), stats(t, "A", "B"), stats(t, "A", "B", "C")
	first := e.put("alice/Kobo", statistics.FileStatistics, v1, "")
	expect(t, "create", first, http.StatusCreated)
	e1 := first.Header().Get("ETag")

	expect(t, "no If-Match on existing", e.put("alice/Kobo", statistics.FileStatistics, v2, ""), http.StatusPreconditionFailed)
	expect(t, "wrong etag", e.put("alice/Kobo", statistics.FileStatistics, v2, `"00000000000000000000000000000000"`), http.StatusPreconditionFailed)
	expect(t, "weak etag", e.put("alice/Kobo", statistics.FileStatistics, v2, "W/"+e1), http.StatusPreconditionFailed)

	second := e.put("alice/Kobo", statistics.FileStatistics, v2, e1)
	expect(t, "update", second, http.StatusNoContent)
	e2 := second.Header().Get("ETag")
	if e2 == "" || e2 == e1 {
		t.Fatalf("new ETag %q", e2)
	}
	expect(t, "stale etag", e.put("alice/Kobo", statistics.FileStatistics, v3, e1), http.StatusPreconditionFailed)

	// Два PUT с одним верным If-Match: проходит ровно один.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Go(func() {
			codes[i] = e.put("alice/Kobo", statistics.FileStatistics, stats(t, "A", "B", fmt.Sprint(i)), e2).Code
		})
	}
	wg.Wait()
	if codes[0]+codes[1] != http.StatusNoContent+http.StatusPreconditionFailed || codes[0] == codes[1] {
		t.Fatalf("concurrent PUT codes %v", codes)
	}

	expect(t, "vocabulary not sqlite", e.put("alice/Kobo", statistics.FileVocabulary, []byte("plain text file"), ""), http.StatusBadRequest)
	expect(t, "broken statistics", e.put("alice/Kobo", statistics.FileStatistics, []byte("SQLite format 3\x00 broken"), "*"), http.StatusBadRequest)
	if !strings.Contains(e.logs.String(), `"msg":"statistics: file rejected"`) {
		t.Error("rejection not logged")
	}
	expect(t, "vocabulary", e.put("alice/Kobo", statistics.FileVocabulary, []byte("SQLite format 3\x00 vocabulary"), ""), http.StatusCreated)
	expect(t, "bad name", e.put("alice/Kobo", "books.epub", v1, ""), http.StatusForbidden)
	expect(t, "subpath", e.put("alice/Kobo", "sub/statistics.sqlite3", v1, ""), http.StatusForbidden)
	expect(t, "root", e.put("alice/Kobo", "", v1, ""), http.StatusForbidden)
	expect(t, "bob creates his own", e.put("bob/Kobo", statistics.FileStatistics, v1, ""), http.StatusCreated)
}

func TestPutTooLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("64 MiB body")
	}
	e := newEnv(t)
	big := make([]byte, statistics.MaxSize+1)
	copy(big, "SQLite format 3\x00")
	expect(t, "Content-Length", e.put("alice/Kobo", statistics.FileVocabulary, big, ""), http.StatusRequestEntityTooLarge)

	// Без Content-Length (chunked) предел ловит MaxBytesReader.
	hr := httptest.NewRequest("PUT", "/dav/vocabulary_builder.sqlite3", io.MultiReader(bytes.NewReader(big)))
	hr.ContentLength = -1
	hr.SetBasicAuth("alice", e.pass["alice/Kobo"])
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	expect(t, "chunked", w, http.StatusRequestEntityTooLarge)
	if _, err := e.stats.Get(e.ctx, 1, statistics.FileVocabulary); err == nil {
		t.Fatal("file stored")
	}
}

// TestSlowUploadOutlivesServerTimeouts: выгрузка дольше ReadTimeout и
// WriteTimeout сервера получает ответ.
func TestSlowUploadOutlivesServerTimeouts(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewUnstartedServer(e.h)
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()

	data := stats(t, "A")
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < len(data); i += len(data) / 4 {
			time.Sleep(200 * time.Millisecond)
			_, _ = pw.Write(data[i:min(i+len(data)/4, len(data))])
		}
		pw.Close()
	}()
	r, _ := http.NewRequest("PUT", srv.URL+"/dav/statistics.sqlite3", pr)
	r.ContentLength = int64(len(data))
	r.SetBasicAuth("alice", e.pass["alice/Kobo"])
	start := time.Now()
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", res.StatusCode)
	}
	if time.Since(start) < 600*time.Millisecond {
		t.Fatalf("upload took %v, test does not exceed server timeouts", time.Since(start))
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t)
	root := func(as string, mod func(*http.Request)) *httptest.ResponseRecorder {
		hr := httptest.NewRequest("PROPFIND", "/dav/", strings.NewReader(propfindBody))
		hr.RemoteAddr = "192.0.2.7:1234"
		if as != "" {
			user, _, _ := strings.Cut(as, "/")
			hr.SetBasicAuth(user, e.pass[as])
		}
		if mod != nil {
			mod(hr)
		}
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, hr)
		return w
	}

	expect(t, "device password", root("alice/Kobo", nil), http.StatusMultiStatus)
	ds, err := e.acc.ListDevices(e.ctx, "alice")
	if err != nil || ds[0].LastUsedAt == nil {
		t.Fatalf("last_used_at not set: %+v %v", ds, err)
	}

	w := root("", nil)
	expect(t, "no header", w, http.StatusUnauthorized)
	if w.Header().Get("WWW-Authenticate") != `Basic realm="koreader-sync-board"` {
		t.Errorf("WWW-Authenticate %q", w.Header().Get("WWW-Authenticate"))
	}
	expect(t, "kosync key", root("", func(r *http.Request) {
		r.SetBasicAuth("alice", accounts.KeyFromPassword(e.pass["alice/Kobo"]))
	}), http.StatusUnauthorized)

	logs := e.logs.String()
	line := regexp.MustCompile(`"msg":"webdav: authorization failed","user":"alice","ip":"192\.0\.2\.7","path":"/dav/","reason":"unauthorized"`)
	if !strings.Contains(logs, `"msg":"webdav: authorization failed","user":"","ip":"192.0.2.7","path":"/dav/","reason":"missing basic credentials"`) ||
		!line.MatchString(logs) {
		t.Fatalf("log:\n%s", logs)
	}
	// Повтор того же неверного ключа не логируется.
	before := strings.Count(e.logs.String(), "authorization failed")
	expect(t, "repeated", root("", func(r *http.Request) {
		r.SetBasicAuth("alice", accounts.KeyFromPassword(e.pass["alice/Kobo"]))
	}), http.StatusUnauthorized)
	if n := strings.Count(e.logs.String(), "authorization failed"); n != before {
		t.Errorf("repeated rejection logged (%d → %d)", before, n)
	}

	if err := e.acc.RevokeDevice(e.ctx, "alice", "Kobo"); err != nil {
		t.Fatal(err)
	}
	expect(t, "revoked", root("alice/Kobo", nil), http.StatusUnauthorized)
}

func TestRevokeBeforeWrite(t *testing.T) {
	e := newEnv(t)
	first := e.put("alice/Kobo", statistics.FileStatistics, stats(t, "A"), "")
	expect(t, "create", first, http.StatusCreated)
	e.h.beforeWrite = func() {
		if err := e.acc.RevokeDevice(e.ctx, "alice", "Kobo"); err != nil {
			t.Error(err)
		}
	}
	expect(t, "revoked during PUT", e.put("alice/Kobo", statistics.FileStatistics, stats(t, "B"), first.Header().Get("ETag")),
		http.StatusUnauthorized)
	e.h.beforeWrite = nil
	f, err := e.stats.Get(e.ctx, 1, statistics.FileStatistics)
	if err != nil || f.ETag != first.Header().Get("ETag") {
		t.Fatalf("file changed: %v", err)
	}
	if !strings.Contains(e.logs.String(), `"reason":"credential changed before write"`) {
		t.Error("not logged")
	}
}

func TestRateLimited(t *testing.T) {
	e := newEnv(t)
	addr := "198.51.100.9:1"
	for i := range ratelimit.MaxFails {
		hr := httptest.NewRequest("PROPFIND", "/dav/", nil)
		hr.RemoteAddr = addr
		hr.SetBasicAuth("alice", fmt.Sprint("wrong-", i))
		e.h.ServeHTTP(httptest.NewRecorder(), hr)
	}
	before := strings.Count(e.logs.String(), "webdav: authorization failed")
	hr := httptest.NewRequest("PROPFIND", "/dav/", nil)
	hr.RemoteAddr = addr
	hr.SetBasicAuth("bob", "another-wrong")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	expect(t, "blocked", w, http.StatusTooManyRequests)
	if ra, err := strconv.Atoi(w.Header().Get("Retry-After")); err != nil || ra <= 0 {
		t.Errorf("Retry-After %q", w.Header().Get("Retry-After"))
	}
	if n := strings.Count(e.logs.String(), "webdav: authorization failed"); n != before {
		t.Errorf("blocked request logged")
	}
}

func TestMethods(t *testing.T) {
	e := newEnv(t)
	w := e.do(req{method: "OPTIONS", path: "/dav/", as: "alice/Kobo"})
	expect(t, "OPTIONS", w, http.StatusOK)
	if w.Header().Get("DAV") != "1" || w.Header().Get("Allow") != "OPTIONS, PROPFIND, GET, HEAD, PUT" {
		t.Errorf("OPTIONS headers %v", w.Header())
	}
	for _, r := range []req{
		{method: "MKCOL", path: "/dav/sub"},
		{method: "DELETE", path: "/dav/statistics.sqlite3"},
		{method: "MOVE", path: "/dav/statistics.sqlite3"},
		{method: "POST", path: "/dav/"},
	} {
		r.as = "alice/Kobo"
		w := e.do(r)
		expect(t, r.method, w, http.StatusMethodNotAllowed)
		if w.Header().Get("Allow") == "" {
			t.Errorf("%s: no Allow", r.method)
		}
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// TestPutBodyReadError: оборванное тело не должно получить 2xx — KOReader счёл бы
// выгрузку успешной.
func TestPutBodyReadError(t *testing.T) {
	e := newEnv(t)
	hr := httptest.NewRequest("PUT", "/dav/statistics.sqlite3", failingBody{})
	hr.SetBasicAuth("alice", e.pass["alice/Kobo"])
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	expect(t, "broken body", w, http.StatusBadRequest)
	if _, err := e.stats.Get(e.ctx, 1, statistics.FileStatistics); err == nil {
		t.Fatal("file stored")
	}
}
