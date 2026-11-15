package webdav

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics"
)

// bookFiles — имена в каталоге диска книг (dir — подкаталог), включая скрытые.
func (e *env) bookFiles(dir string) []string {
	e.t.Helper()
	es, err := os.ReadDir(filepath.Join(e.books, dir))
	if err != nil {
		e.t.Fatal(err)
	}
	out := []string{}
	for _, f := range es {
		out = append(out, f.Name())
	}
	return out
}

func (e *env) putBook(path string, body []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(req{method: "PUT", path: path, body: body, as: "alice/Kobo"})
}

func TestBooksPut(t *testing.T) {
	e := newEnv(t)
	w := e.putBook("/dav/books/Пикник.epub", []byte("first"))
	expect(t, "create", w, http.StatusCreated)
	etag := w.Header().Get("ETag")

	// PROPFIND x/net/webdav отдаёт тот же ETag.
	pf := e.do(req{method: "PROPFIND", path: "/dav/books/Пикник.epub", as: "bob/Kobo", headers: map[string]string{"Depth": "0"}})
	expect(t, "propfind", pf, http.StatusMultiStatus)
	if got := listFolder(t, pf.Body.String()); len(got) != 1 || got[0].etag != etag || got[0].size != "5" {
		t.Fatalf("propfind %+v, etag %s", got, etag)
	}

	expect(t, "replace", e.putBook("/dav/books/Пикник.epub", []byte("second")), http.StatusNoContent)
	if b, _ := os.ReadFile(filepath.Join(e.books, "Пикник.epub")); string(b) != "second" {
		t.Fatalf("content %q", b)
	}
	// Служебные файлы macOS пишутся.
	expect(t, "dot file", e.putBook("/dav/books/._Пикник.epub", []byte("x")), http.StatusCreated)
	if n := e.changed.Load(); n != 3 {
		t.Fatalf("changed %d times", n)
	}

	expect(t, "no parent", e.putBook("/dav/books/нет/a.epub", []byte("x")), http.StatusConflict)
	expect(t, "collection", e.putBook("/dav/books/", []byte("x")), http.StatusMethodNotAllowed)
	if n := e.changed.Load(); n != 3 {
		t.Fatalf("failed requests triggered a scan: %d", n)
	}
	if got := e.bookFiles(""); !slices.Equal(got, []string{"._Пикник.epub", "Пикник.epub"}) {
		t.Fatalf("files %v", got)
	}
}

// Папка книг не трогает статистику: statistics.sqlite3 в ней — обычный файл.
func TestBooksStatisticsName(t *testing.T) {
	e := newEnv(t)
	expect(t, "put", e.putBook("/dav/books/statistics.sqlite3", []byte("not a database")), http.StatusCreated)
	if _, err := e.stats.Get(e.ctx, 1, statistics.FileStatistics); !errors.Is(err, statistics.ErrNotFound) {
		t.Fatalf("statistics: %v", err)
	}
	expect(t, "GET /dav/", e.do(req{method: "GET", path: "/dav/statistics.sqlite3", as: "alice/Kobo"}), http.StatusNotFound)
	if got := e.bookFiles(""); !slices.Equal(got, []string{"statistics.sqlite3"}) {
		t.Fatalf("files %v", got)
	}
}

// partialBody отдаёт часть данных и обрывается.
type partialBody struct{ n int }

func (b *partialBody) Read(p []byte) (int, error) {
	if b.n <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := min(len(p), b.n)
	b.n -= n
	return n, nil
}

func TestBooksPutAborted(t *testing.T) {
	e := newEnv(t)
	expect(t, "create", e.putBook("/dav/books/a.epub", []byte("original")), http.StatusCreated)
	hr := httptest.NewRequest("PUT", "/dav/books/a.epub", &partialBody{n: 32 << 10})
	hr.SetBasicAuth("alice", e.pass["alice/Kobo"])
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	expect(t, "aborted", w, http.StatusBadRequest)
	if b, _ := os.ReadFile(filepath.Join(e.books, "a.epub")); string(b) != "original" {
		t.Fatalf("target changed: %q", b)
	}
	if got := e.bookFiles(""); !slices.Equal(got, []string{"a.epub"}) {
		t.Fatalf("files %v", got)
	}
	if n := e.changed.Load(); n != 1 {
		t.Fatalf("changed %d times", n)
	}
}

func TestBooksTooLarge(t *testing.T) {
	e := newEnv(t)
	hr := httptest.NewRequest("PUT", "/dav/books/a.epub", strings.NewReader("x"))
	hr.ContentLength = 2 << 30
	hr.SetBasicAuth("alice", e.pass["alice/Kobo"])
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	expect(t, "too large", w, http.StatusRequestEntityTooLarge)
	if got := e.bookFiles(""); len(got) != 0 {
		t.Fatalf("files %v", got)
	}
}

func TestBooksFolders(t *testing.T) {
	e := newEnv(t)
	expect(t, "MKCOL", e.do(req{method: "MKCOL", path: "/dav/books/Стругацкие", as: "alice/Kobo"}), http.StatusCreated)
	expect(t, "PUT", e.putBook("/dav/books/Стругацкие/Пикник.fb2", []byte("fb2")), http.StatusCreated)
	if got := e.bookFiles("Стругацкие"); !slices.Equal(got, []string{"Пикник.fb2"}) {
		t.Fatalf("files %v", got)
	}
	// Все пользователи видят одну папку.
	w := e.do(req{method: "PROPFIND", path: "/dav/books/Стругацкие/", as: "bob/Kobo", headers: map[string]string{"Depth": "1"}})
	expect(t, "PROPFIND", w, http.StatusMultiStatus)
	if got := listFolder(t, w.Body.String()); len(got) != 2 || got[1].size != "3" {
		t.Fatalf("listing %+v", got)
	}

	w = e.do(req{method: "MOVE", path: "/dav/books/Стругацкие/Пикник.fb2", as: "alice/Kobo",
		headers: map[string]string{"Destination": "/dav/books/Пикник.fb2"}})
	expect(t, "MOVE", w, http.StatusCreated)
	w = e.do(req{method: "COPY", path: "/dav/books/Пикник.fb2", as: "alice/Kobo",
		headers: map[string]string{"Destination": "/dav/books/Копия.fb2"}})
	expect(t, "COPY", w, http.StatusCreated)
	expect(t, "DELETE", e.do(req{method: "DELETE", path: "/dav/books/Стругацкие", as: "alice/Kobo"}), http.StatusNoContent)
	if got := e.bookFiles(""); !slices.Equal(got, []string{"Копия.fb2", "Пикник.fb2"}) {
		t.Fatalf("files %v", got)
	}
	if n := e.changed.Load(); n != 5 {
		t.Fatalf("changed %d times", n)
	}

	// Перенос наружу отклоняется, файл на месте.
	w = e.do(req{method: "MOVE", path: "/dav/books/Пикник.fb2", as: "alice/Kobo",
		headers: map[string]string{"Destination": "/dav/statistics.sqlite3"}})
	expect(t, "MOVE outside", w, http.StatusNotFound)
	if got := e.bookFiles(""); !slices.Equal(got, []string{"Копия.fb2", "Пикник.fb2"}) {
		t.Fatalf("files %v", got)
	}
	// Чтение и блокировки скан не ставят.
	for _, m := range []string{"GET", "HEAD", "PROPFIND", "OPTIONS"} {
		e.do(req{method: m, path: "/dav/books/Пикник.fb2", as: "alice/Kobo"})
	}
	if n := e.changed.Load(); n != 5 {
		t.Fatalf("reads triggered a scan: %d", n)
	}
}

func TestBooksOptionsAndAuth(t *testing.T) {
	e := newEnv(t)
	w := e.do(req{method: "OPTIONS", path: "/dav/books/", as: "alice/Kobo"})
	expect(t, "OPTIONS", w, http.StatusOK)
	if w.Header().Get("DAV") != "1, 2" {
		t.Fatalf("DAV %q", w.Header().Get("DAV"))
	}
	hr := httptest.NewRequest("PROPFIND", "/dav/books/", nil)
	hr.SetBasicAuth("alice", "wrong")
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	expect(t, "wrong password", w, http.StatusUnauthorized)
	if !strings.Contains(e.logs.String(), "webdav: authorization failed") {
		t.Fatalf("log %s", e.logs.String())
	}
	expect(t, "anonymous PUT", e.do(req{method: "PUT", path: "/dav/books/a.epub", body: []byte("x")}), http.StatusUnauthorized)
	if got := e.bookFiles(""); len(got) != 0 {
		t.Fatalf("files %v", got)
	}
}

const lockBody = `<?xml version="1.0" encoding="utf-8" ?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
	`<D:locktype><D:write/></D:locktype><D:owner>finder</D:owner></D:lockinfo>`

// LOCK на новый ресурс создаёт пустой файл; в индекс он не попадает, а после записи — попадает.
func TestBooksLockEmptyFile(t *testing.T) {
	e := newEnv(t)
	lib := library.NewStore(e.db)
	if err := lib.EnsureManagedRoot(e.ctx, library.ManagedUploads, e.books); err != nil {
		t.Fatal(err)
	}
	sc := library.NewScanner(lib, 0, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { sc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	scan := func() library.Summary {
		t.Helper()
		sc.TriggerUploads()
		deadline := time.Now().Add(5 * time.Second)
		for {
			s, err := sc.Summary(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !s.Running {
				return s
			}
			if time.Now().After(deadline) {
				t.Fatal("scan did not finish")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	w := e.do(req{method: "LOCK", path: "/dav/books/a.epub", body: []byte(lockBody), as: "alice/Kobo",
		headers: map[string]string{"Timeout": "Second-60"}})
	expect(t, "LOCK", w, http.StatusCreated)
	if fi, err := os.Stat(filepath.Join(e.books, "a.epub")); err != nil || fi.Size() != 0 {
		t.Fatalf("locked file: %v, %v", fi, err)
	}
	if s := scan(); s.Files != 0 {
		t.Fatalf("empty file indexed: %+v", s)
	}
	expect(t, "PUT", e.putBook("/dav/books/a.epub", []byte("content")), http.StatusNoContent)
	if s := scan(); s.Files != 1 {
		t.Fatalf("written file not indexed: %+v", s)
	}
}

// Скачивание дольше WriteTimeout сервера не обрывается.
func TestBooksSlowDownload(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<18) // 4 МиБ
	if err := os.WriteFile(filepath.Join(e.books, "big.pdf"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(e.h)
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()
	r, _ := http.NewRequest("GET", srv.URL+"/dav/books/big.pdf", nil)
	r.SetBasicAuth("alice", e.pass["alice/Kobo"])
	res, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	start := time.Now()
	var got bytes.Buffer
	buf := make([]byte, 256<<10)
	for {
		n, err := res.Body.Read(buf)
		got.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("after %d bytes: %v", got.Len(), err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("got %d bytes, want %d", got.Len(), len(data))
	}
	if time.Since(start) < 600*time.Millisecond {
		t.Fatalf("download took %v, test does not exceed the server timeout", time.Since(start))
	}
}
