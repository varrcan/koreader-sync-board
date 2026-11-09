package opds

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/convert"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

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

const testEPUB = "../library/meta/testdata/epub2.epub"

type env struct {
	t       *testing.T
	ctx     context.Context
	db      *store.DB
	acc     *accounts.Service
	lib     *library.Store
	covers  *library.Covers
	h       *Handler
	url     string
	pass    string
	dataDir string
	root    string
	books   map[string]meta.Book
	logs    *logBuffer
	log     *slog.Logger
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
	if _, err := acc.CreateUser(ctx, "alice", accounts.RoleUser); err != nil {
		t.Fatal(err)
	}
	_, pass, err := acc.AddDevice(ctx, "alice", "Kobo")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, db: db, acc: acc, lib: library.NewStore(db), pass: pass, dataDir: t.TempDir(),
		root: t.TempDir(), books: map[string]meta.Book{}, logs: logs, log: log}
	e.covers = library.NewCovers(filepath.Join(e.dataDir, "covers"))
	e.h = New(acc, clientip.New(nil), Library{Store: e.lib, Covers: e.covers}, log)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	return e
}

// useConverter подключает конвертер с заданной программой дочернего процесса.
func (e *env) useConverter(exe string, opts ...convert.Option) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.ctx)
	c, err := convert.New(ctx, e.lib, e.dataDir, e.log, append([]convert.Option{convert.WithExecutable(exe)}, opts...)...)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { cancel(); c.Wait(10 * time.Second) })
	e.h.lib.Converter = c
}

// scan индексирует файлы name → содержимое в корне; метаданные берутся из e.books.
func (e *env) scan(files map[string]string) {
	e.t.Helper()
	if roots, err := e.lib.Roots(e.ctx); err != nil {
		e.t.Fatal(err)
	} else if len(roots) == 0 {
		if _, err := e.lib.AddRoot(e.ctx, e.root, ""); err != nil {
			e.t.Fatal(err)
		}
	}
	for name, content := range files {
		path := filepath.Join(e.root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	parse := func(path, _ string) (meta.Book, error) { return e.books[filepath.Base(path)], nil }
	sc := library.NewScanner(e.lib, 0, slog.New(slog.DiscardHandler), library.WithReader(hashing.PartialMD5, parse))
	ctx, stop := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { sc.Run(ctx); close(done) }()
	defer func() { stop(); <-done }()
	sc.Trigger()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		sum, err := sc.Summary(e.ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		if !sum.Running {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatal("scan did not finish")
		}
	}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// request выполняет запрос к каталогу. Пустой pass — без Authorization.
func (e *env) request(method, target, pass string, headers map[string]string) response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+target, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if pass != "" {
		req.SetBasicAuth("alice", pass)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return response{status: res.StatusCode, header: res.Header, body: body}
}

func (e *env) get(target string) response {
	e.t.Helper()
	return e.request("GET", target, e.pass, nil)
}

func (r response) expect(t *testing.T, name string, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("%s: status %d, want %d (%s)", name, r.status, status, r.body)
	}
}

// feedAt запрашивает ленту и разбирает её.
func (e *env) feedAt(target string) feed {
	e.t.Helper()
	r := e.get(target)
	r.expect(e.t, target, 200)
	var f feed
	if err := xml.Unmarshal(r.body, &f); err != nil {
		e.t.Fatalf("%s: %v\n%s", target, err, r.body)
	}
	return f
}

func findLink(links []link, rel string) *link {
	for i := range links {
		if links[i].Rel == rel {
			return &links[i]
		}
	}
	return nil
}

func (e *env) failures() int {
	return strings.Count(e.logs.String(), `"msg":"opds: authorization failed"`)
}

func TestAuthMissingHeader(t *testing.T) {
	e := newEnv(t)
	// Больше предела ограничителя: запрос без пароля не расходует бюджет.
	for range ratelimit.MaxFails + 5 {
		r := e.request("GET", "/opds", "", nil)
		r.expect(t, "no header", 401)
		if !strings.HasPrefix(r.header.Get("WWW-Authenticate"), "Basic") {
			t.Fatalf("WWW-Authenticate %q", r.header.Get("WWW-Authenticate"))
		}
	}
	if strings.Contains(e.logs.String(), "authorization failed") {
		t.Fatalf("logged:\n%s", e.logs)
	}
	e.get("/opds").expect(t, "device password", 200)
}

func TestAuthWrongPassword(t *testing.T) {
	e := newEnv(t)
	e.request("GET", "/opds", "wrong-password", nil).expect(t, "wrong", 401)
	if n := e.failures(); n != 1 {
		t.Fatalf("%d log lines:\n%s", n, e.logs)
	}
	e.request("GET", "/opds", "wrong-password", nil).expect(t, "repeat", 401)
	if n := e.failures(); n != 1 {
		t.Fatalf("repeat is logged:\n%s", e.logs)
	}
}

func TestAuthPanelPasswordRejected(t *testing.T) {
	e := newEnv(t)
	const panel = "panel-password-1"
	if err := e.acc.SetPanelPassword(e.ctx, "alice", panel, 0); err != nil {
		t.Fatal(err)
	}
	e.request("GET", "/opds", panel, nil).expect(t, "panel password", 401)
	if e.failures() != 1 {
		t.Fatalf("no log line:\n%s", e.logs)
	}
}

func TestAuthRevokedDevice(t *testing.T) {
	e := newEnv(t)
	e.get("/opds/books").expect(t, "before", 200)
	if err := e.acc.RevokeDevice(e.ctx, "alice", "Kobo"); err != nil {
		t.Fatal(err)
	}
	e.get("/opds/books").expect(t, "revoked", 401)
}

func TestAuthRateLimited(t *testing.T) {
	e := newEnv(t)
	for i := range ratelimit.MaxFails + 1 {
		r := e.request("GET", "/opds", fmt.Sprintf("wrong-%d", i), nil)
		if r.status == 429 {
			if r.header.Get("Retry-After") == "" {
				t.Fatalf("no Retry-After: %v", r.header)
			}
			return
		}
		r.expect(t, "wrong", 401)
	}
	t.Fatal("not blocked")
}

func TestRoutesAndMethods(t *testing.T) {
	e := newEnv(t)
	e.request("GET", "/opds/unknown", "", nil).expect(t, "unknown without password", 401)
	e.get("/opds/unknown").expect(t, "unknown", 404)
	r := e.request("POST", "/opds/books", e.pass, nil)
	r.expect(t, "post", 405)
	if got := r.header.Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow %q", got)
	}
}

func TestFeedStructure(t *testing.T) {
	e := newEnv(t)
	for _, target := range []string{"/opds", "/opds/", "/opds/books", "/opds/search?q=x"} {
		r := e.get(target)
		r.expect(t, target, 200)
		if r.body[0] != '<' {
			t.Fatalf("%s: first byte %q", target, r.body[0])
		}
		if r.header.Get("Content-Encoding") != "" || r.header.Get("ETag") != "" || r.header.Get("Last-Modified") != "" {
			t.Fatalf("%s: headers %v", target, r.header)
		}
		f := e.feedAt(target)
		for _, rel := range []string{"self", "start", "search"} {
			if findLink(f.Links, rel) == nil {
				t.Fatalf("%s: no %s link", target, rel)
			}
		}
		for _, l := range f.Links {
			if !strings.HasPrefix(l.Href, "/opds") {
				t.Fatalf("%s: link %+v", target, l)
			}
		}
		if f.ID == "" || f.Title == "" || f.Updated == "" {
			t.Fatalf("%s: feed %+v", target, f)
		}
	}
	if got := e.get("/opds").header.Get("Content-Type"); got != "application/atom+xml;profile=opds-catalog;kind=navigation" {
		t.Fatalf("root type %q", got)
	}
	if got := e.get("/opds/books").header.Get("Content-Type"); got != "application/atom+xml;profile=opds-catalog;kind=acquisition" {
		t.Fatalf("books type %q", got)
	}
}

func TestRootFeed(t *testing.T) {
	e := newEnv(t)
	f := e.feedAt("/opds")
	if len(f.Entries) != 1 || f.Entries[0].Title != "All books" {
		t.Fatalf("entries %+v", f.Entries)
	}
	sub := findLink(f.Entries[0].Links, "subsection")
	if sub == nil || sub.Href != "/opds/books" || sub.Type != acquisitionType {
		t.Fatalf("subsection %+v", f.Entries[0].Links)
	}
	// KOReader v2026.07.1 пропускает шаблон, если описание OpenSearch встретилось раньше.
	opensearch, template := -1, -1
	for i, l := range f.Links {
		switch {
		case l.Rel == "search" && l.Type == "application/opensearchdescription+xml" && l.Href == "/opds/opensearch.xml":
			opensearch = i
		case l.Rel == "search" && l.Type == searchTemplateType && l.Href == "/opds/search?q={searchTerms}":
			template = i
		}
	}
	if opensearch < 0 || template < 0 || opensearch > template {
		t.Fatalf("search links %+v", f.Links)
	}
}

func TestSearchTemplateInEveryFeed(t *testing.T) {
	e := newEnv(t)
	for _, target := range []string{"/opds/books", "/opds/search?q=x"} {
		f := e.feedAt(target)
		links := 0
		for _, l := range f.Links {
			if l.Rel == "search" {
				links++
				if l.Href != "/opds/search?q={searchTerms}" || l.Type != searchTemplateType {
					t.Fatalf("%s: %+v", target, l)
				}
			}
		}
		if links != 1 {
			t.Fatalf("%s: %d search links, OpenSearch belongs to the root only", target, links)
		}
	}
}

func TestOpenSearchDescription(t *testing.T) {
	e := newEnv(t)
	r := e.get("/opds/opensearch.xml")
	r.expect(t, "opensearch", 200)
	if got := r.header.Get("Content-Type"); got != "application/opensearchdescription+xml" {
		t.Fatalf("type %q", got)
	}
	var d struct {
		XMLName xml.Name `xml:"OpenSearchDescription"`
		URL     struct {
			Type     string `xml:"type,attr"`
			Template string `xml:"template,attr"`
		} `xml:"Url"`
	}
	if err := xml.Unmarshal(r.body, &d); err != nil {
		t.Fatalf("%v\n%s", err, r.body)
	}
	if d.URL.Template != "/opds/search?q={searchTerms}" || d.URL.Type != searchTemplateType {
		t.Fatalf("%+v", d)
	}
}

func TestHeadFeed(t *testing.T) {
	e := newEnv(t)
	r := e.request("HEAD", "/opds/books", e.pass, nil)
	r.expect(t, "head", 200)
	if len(r.body) != 0 || r.header.Get("Content-Type") != acquisitionType {
		t.Fatalf("%v %q", r.header, r.body)
	}
}

func TestEmptyFeed(t *testing.T) {
	e := newEnv(t)
	if f := e.feedAt("/opds/books"); len(f.Entries) != 0 || findLink(f.Links, "next") != nil {
		t.Fatalf("%+v", f)
	}
	if f := e.feedAt("/opds/search?q=nothing"); len(f.Entries) != 0 || findLink(f.Links, "search") == nil {
		t.Fatalf("%+v", f)
	}
}

func TestBadParams(t *testing.T) {
	e := newEnv(t)
	for _, before := range []string{"abc", "0", "-1", ""} {
		e.get("/opds/books?before="+before).expect(t, "before="+before, 400)
		e.get("/opds/search?q=x&before="+before).expect(t, "search before="+before, 400)
	}
	e.get("/opds/search?q="+strings.Repeat("я", maxSearchLength)).expect(t, "at limit", 200)
	e.get("/opds/search?q="+strings.Repeat("я", maxSearchLength+1)).expect(t, "too long", 400)
}

func (e *env) manyBooks(prefix string, n int) {
	e.t.Helper()
	files := map[string]string{}
	for i := range n {
		name := fmt.Sprintf("%s%03d.epub", prefix, i)
		e.books[name] = meta.Book{Title: fmt.Sprintf("%s %03d", prefix, i), Authors: []string{name}}
		files[name] = name
	}
	e.scan(files)
}

func TestPagination(t *testing.T) {
	e := newEnv(t)
	e.manyBooks("book", 45)
	first := e.feedAt("/opds/books")
	if len(first.Entries) != pageSize {
		t.Fatalf("first page: %d", len(first.Entries))
	}
	next := findLink(first.Links, "next")
	if next == nil || next.Type != acquisitionType || !strings.HasPrefix(next.Href, "/opds/books?before=") {
		t.Fatalf("next %+v", next)
	}
	second := e.feedAt(next.Href)
	if len(second.Entries) != 15 || findLink(second.Links, "next") != nil {
		t.Fatalf("second page: %d, %+v", len(second.Entries), second.Links)
	}
	seen := map[string]bool{}
	for _, entry := range append(first.Entries, second.Entries...) {
		if seen[entry.ID] {
			t.Fatalf("duplicate %s", entry.ID)
		}
		seen[entry.ID] = true
	}
	if self := findLink(second.Links, "self"); self == nil || self.Href != next.Href {
		t.Fatalf("self %+v, want %s", self, next.Href)
	}
}

func TestSearch(t *testing.T) {
	e := newEnv(t)
	e.books["elka.epub"] = meta.Book{Title: "Ёлка", Authors: []string{"Зощенко"}}
	e.books["other.epub"] = meta.Book{Title: "Другая", Authors: []string{"Автор"}}
	e.scan(map[string]string{"elka.epub": "e", "other.epub": "o"})
	f := e.feedAt("/opds/search?q=%D0%B5%D0%BB%D0%BA%D0%B0")
	if len(f.Entries) != 1 || f.Entries[0].Title != "Ёлка" {
		t.Fatalf("%+v", f.Entries)
	}
}

func TestSearchNextKeepsQuery(t *testing.T) {
	e := newEnv(t)
	e.manyBooks("a&b", 31)
	r := e.get("/opds/search?q=a%26b")
	r.expect(t, "search", 200)
	if !bytes.Contains(r.body, []byte("&amp;")) {
		t.Fatalf("href is not escaped:\n%s", r.body)
	}
	first := e.feedAt("/opds/search?q=a%26b")
	if len(first.Entries) != pageSize {
		t.Fatalf("first page: %d", len(first.Entries))
	}
	next := findLink(first.Links, "next")
	if next == nil {
		t.Fatal("no next")
	}
	second := e.feedAt(next.Href)
	if len(second.Entries) != 1 || findLink(second.Links, "next") != nil {
		t.Fatalf("second page %+v", second)
	}
}

func TestFormatTypes(t *testing.T) {
	e := newEnv(t)
	want := map[string]string{
		"epub": "application/epub+zip", "fb2": "application/x-fictionbook+xml", "fb2.zip": "application/fb2+zip",
		"pdf": "application/pdf", "djvu": "image/vnd.djvu", "mobi": "application/x-mobipocket-ebook",
		"azw": "application/vnd.amazon.mobi8-ebook", "cbz": "application/vnd.comicbook+zip",
	}
	files := map[string]string{}
	for format := range want {
		name := "f." + format
		e.books[name] = meta.Book{Title: format, Authors: []string{format}}
		files[name] = format
	}
	e.scan(files)
	f := e.feedAt("/opds/books")
	if len(f.Entries) != len(want) {
		t.Fatalf("%d entries", len(f.Entries))
	}
	for _, entry := range f.Entries {
		l := findLink(entry.Links, acquisitionRel)
		if l == nil || l.Type != want[entry.Title] {
			t.Errorf("%s: %+v", entry.Title, l)
			continue
		}
		// Точка в адресе дала бы KOReader суффикс «zip» для fb2.zip.
		if strings.Contains(l.Href, ".") {
			t.Errorf("%s: href with a dot %s", entry.Title, l.Href)
		}
		if l.Title != strings.ToUpper(entry.Title) || l.Length != int64(len(entry.Title)) {
			t.Errorf("%s: %+v", entry.Title, l)
		}
	}
}

func TestEntryBothFormats(t *testing.T) {
	e := newEnv(t)
	e.books["a.epub"] = meta.Book{Title: "A", Authors: []string{"X"}}
	e.books["a.fb2"] = meta.Book{Title: "A", Authors: []string{"X"}}
	e.scan(map[string]string{"a.epub": "1", "a.fb2": "2"})
	f := e.feedAt("/opds/books")
	if len(f.Entries) != 1 {
		t.Fatalf("%+v", f.Entries)
	}
	types := map[string]bool{}
	for _, l := range f.Entries[0].Links {
		if l.Rel == acquisitionRel {
			types[l.Type] = true
		}
	}
	if len(types) != 2 || !types["application/epub+zip"] || !types["application/x-fictionbook+xml"] {
		t.Fatalf("%+v", f.Entries[0].Links)
	}
}

func TestEntryFallbackTitle(t *testing.T) {
	e := newEnv(t)
	e.scan(map[string]string{"Война и мир.epub": "1", "Анна.fb2.zip": "2"})
	f := e.feedAt("/opds/books")
	titles := map[string]bool{}
	for _, entry := range f.Entries {
		titles[entry.Title] = true
		if entry.Author != nil {
			t.Fatalf("author %+v", entry.Author)
		}
		if entry.ID == "" || entry.Updated == "" {
			t.Fatalf("entry %+v", entry)
		}
	}
	if !titles["Война и мир"] || !titles["Анна"] {
		t.Fatalf("%v", titles)
	}
}

func (e *env) bookID() int64 {
	e.t.Helper()
	p, err := e.lib.Books(e.ctx, "", 0, 1)
	if err != nil || len(p.Items) == 0 {
		e.t.Fatalf("%v, %+v", err, p)
	}
	return p.Items[0].ID
}

func TestEntryText(t *testing.T) {
	e := newEnv(t)
	e.books["a.epub"] = meta.Book{Title: "A <&>", Authors: []string{"Толкин, Дж. Р. Р."}}
	e.scan(map[string]string{"a.epub": "1"})
	if err := e.lib.SetMetadata(e.ctx, e.bookID(), library.MetadataEdit{Title: "A <&>", Authors: "Толкин, Дж. Р. Р.",
		Description: "до\x01после"}); err != nil {
		t.Fatal(err)
	}
	f := e.feedAt("/opds/books")
	entry := f.Entries[0]
	if entry.Title != "A <&>" || entry.Author == nil || entry.Author.Name != "Толкин, Дж. Р. Р." {
		t.Fatalf("%+v", entry)
	}
	if entry.Summary != "до�после" {
		t.Fatalf("summary %q", entry.Summary)
	}
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	// Самый маленький корректный JPEG не нужен: содержимое здесь не разбирается.
	return []byte("\xff\xd8\xff\xe0 thumbnail \xff\xd9")
}

func TestCovers(t *testing.T) {
	e := newEnv(t)
	e.books["a.epub"] = meta.Book{Title: "A", Authors: []string{"X"}}
	e.scan(map[string]string{"a.epub": "1"})
	if f := e.feedAt("/opds/books"); findLink(f.Entries[0].Links, imageRel) != nil || findLink(f.Entries[0].Links, thumbnailRel) != nil {
		t.Fatalf("cover links without a cover: %+v", f.Entries[0].Links)
	}

	var cover string
	if err := e.covers.Attach(jpegBytes(t), func(id string) error {
		cover = id
		return e.lib.SetMetadata(e.ctx, e.bookID(), library.MetadataEdit{Title: "A", Authors: "X", Cover: &id})
	}); err != nil {
		t.Fatal(err)
	}
	f := e.feedAt("/opds/books")
	for _, rel := range []string{imageRel, thumbnailRel} {
		l := findLink(f.Entries[0].Links, rel)
		if l == nil || l.Href != "/opds/covers/"+cover || l.Type != "image/jpeg" {
			t.Fatalf("%s: %+v", rel, l)
		}
	}
	r := e.get("/opds/covers/" + cover)
	r.expect(t, "cover", 200)
	if r.header.Get("Content-Type") != "image/jpeg" || !strings.HasPrefix(r.header.Get("Cache-Control"), "private") ||
		!bytes.Equal(r.body, jpegBytes(t)) {
		t.Fatalf("%v", r.header)
	}
	e.get("/opds/covers/"+strings.Repeat("0", len(cover))).expect(t, "unknown cover", 404)
	e.get("/opds/covers/..%2Fx").expect(t, "bad cover", 404)
}
