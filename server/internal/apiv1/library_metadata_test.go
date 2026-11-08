package apiv1

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/library/lookup"
)

const metadataFB2 = `<?xml version="1.0" encoding="UTF-8"?><FictionBook><description><title-info>
	<author><first-name>Дэн</first-name><last-name>Симмонс</last-name></author>
	<book-title>Гиперион</book-title><annotation><p>Первый.</p><p>Второй.</p></annotation><lang>ru</lang></title-info>
	<publish-info><isbn>978-5-17-088647-0</isbn></publish-info></description></FictionBook>`

type stubLookup struct {
	mu      sync.Mutex
	queries []lookup.Query
	result  lookup.Result
}

func (s *stubLookup) Search(_ context.Context, q lookup.Query) lookup.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, q)
	return s.result
}

func (s *stubLookup) last(t *testing.T) lookup.Query {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		t.Fatal("no provider queries")
	}
	return s.queries[len(s.queries)-1]
}

type stubCover struct {
	data []byte
	err  error
	urls []string
}

func (s *stubCover) Fetch(_ context.Context, u string) ([]byte, error) {
	s.urls = append(s.urls, u)
	return s.data, s.err
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type metadataEnv struct {
	*env
	admin  *browser
	lookup *stubLookup
	cover  *stubCover
	root   string
	id     int64
	path   string
}

// newMetadataEnv — администратор и одна книга hyperion.fb2.
func newMetadataEnv(t *testing.T, files map[string]string, name string) *metadataEnv {
	t.Helper()
	e := newEnv(t)
	st := &stubLookup{result: lookup.Result{Items: []lookup.Candidate{}, Providers: []lookup.Status{{ID: "fantlab", Status: "ok"}}}}
	cv := &stubCover{data: testJPEG(t)}
	e.srv.Library.Lookup, e.srv.Library.CoverSource = st, cv
	admin := e.panelUser("root", accounts.RoleAdmin)
	dir := libraryFixture(t, admin, files)
	id := libraryIDs(t, admin)[name]
	return &metadataEnv{env: e, admin: admin, lookup: st, cover: cv, root: dir, id: id,
		path: fmt.Sprintf("/api/v1/library/books/%d/metadata", id)}
}

func hyperion(t *testing.T) *metadataEnv {
	t.Helper()
	return newMetadataEnv(t, map[string]string{"hyperion.fb2": metadataFB2}, "hyperion.fb2")
}

func TestMetadataAccess(t *testing.T) {
	e := hyperion(t)
	anon := e.browser()
	user := e.panelUser("alice", accounts.RoleUser)
	for _, c := range [][2]string{{"GET", e.path + "/candidates"}, {"PUT", e.path}, {"DELETE", e.path}} {
		anon.do(c[0], c[1], map[string]string{}).expect(t, "anon "+c[0], 401, "unauthorized")
		user.do(c[0], c[1], map[string]string{"title": "x"}).expect(t, "user "+c[0], 403, "forbidden")
	}
	if len(e.lookup.queries) != 0 {
		t.Fatal("providers queried for a non-admin")
	}
	if n := e.count(`SELECT count(*) FROM library_book_meta`); n != 0 {
		t.Fatal("edit saved by a non-admin")
	}
}

func TestMetadataNotFound(t *testing.T) {
	e := hyperion(t)
	const p = "/api/v1/library/books/9999/metadata"
	e.admin.do("GET", p+"/candidates", nil).expect(t, "candidates", 404, "not_found")
	e.admin.do("PUT", p, map[string]string{"title": "x"}).expect(t, "put", 404, "not_found")
	e.admin.do("PUT", p, map[string]string{"title": "x", "cover_url": "https://fantlab.ru/a"}).expect(t, "put with cover", 404, "not_found")
	if len(e.cover.urls) != 0 {
		t.Fatal("cover fetched for a missing book")
	}
	e.admin.do("DELETE", p, nil).expect(t, "delete", 404, "not_found")
}

func TestCandidatesParams(t *testing.T) {
	e := hyperion(t)
	c := e.path + "/candidates"
	e.admin.do("GET", c+"?q="+strings.Repeat("я", 201), nil).expect(t, "long q", 400, "bad_request")
	e.admin.do("GET", c+"?isbn=123", nil).expect(t, "bad isbn", 400, "bad_request")
	if len(e.lookup.queries) != 0 {
		t.Fatal("providers queried for a bad request")
	}

	r := e.admin.do("GET", c, nil)
	r.expect(t, "auto", 200, "")
	want := lookup.Query{ISBN: "9785170886470", Text: "Гиперион Дэн Симмонс", Language: "ru"}
	if got := e.lookup.last(t); got != want {
		t.Fatalf("auto query %+v", got)
	}
	if r.body["query"] != want.Text || r.body["isbn"] != want.ISBN {
		t.Fatalf("response %v", r.body)
	}

	e.admin.do("GET", c+"?q="+url.QueryEscape("Гиперион Симмонс"), nil).expect(t, "q only", 200, "")
	if got := e.lookup.last(t); got.ISBN != "" || got.Text != "Гиперион Симмонс" {
		t.Fatalf("q only: %+v", got)
	}

	e.admin.do("GET", c+"?isbn=0-306-40615-2", nil).expect(t, "isbn only", 200, "")
	if got := e.lookup.last(t); got.ISBN != "0306406152" || got.Text != "" {
		t.Fatalf("isbn only: %+v", got)
	}
}

func firstItem(t *testing.T, r resp) map[string]any {
	t.Helper()
	items, _ := r.body["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no items: %v", r.body)
	}
	return items[0].(map[string]any)
}

func TestCandidateFromFile(t *testing.T) {
	e := hyperion(t)
	e.lookup.result.Items = []lookup.Candidate{{Provider: "fantlab", Match: "isbn", Title: "Из каталога"}}
	r := e.admin.do("GET", e.path+"/candidates", nil)
	r.expect(t, "candidates", 200, "")
	first := firstItem(t, r)
	if first["provider"] != "file" || first["match"] != "file" || first["title"] != "Гиперион" ||
		first["description"] != "Первый.\n\nВторой." || first["isbn"] != "9785170886470" || first["authors"] != "Дэн Симмонс" {
		t.Fatalf("file candidate %v", first)
	}
	if first["year"] != nil || first["cover_url"] != nil {
		t.Fatalf("absent fields %v", first)
	}
	items := r.body["items"].([]any)
	if len(items) != 2 || items[1].(map[string]any)["title"] != "Из каталога" {
		t.Fatalf("items %v", items)
	}
}

func TestCandidatesWithoutTitleUseFilename(t *testing.T) {
	e := newMetadataEnv(t, map[string]string{"Solaris_1961.pdf": "pdf"}, "Solaris_1961.pdf")
	r := e.admin.do("GET", e.path+"/candidates", nil)
	r.expect(t, "candidates", 200, "")
	if r.body["query"] != "Solaris_1961" || r.body["isbn"] != nil {
		t.Fatalf("%v", r.body)
	}
	if items := r.body["items"].([]any); len(items) != 0 {
		t.Fatalf("items %v", items)
	}
}

func TestCandidatesUnavailableRoot(t *testing.T) {
	e := hyperion(t)
	if err := os.RemoveAll(e.root); err != nil {
		t.Fatal(err)
	}
	r := e.admin.do("GET", e.path+"/candidates", nil)
	r.expect(t, "candidates", 200, "")
	if items := r.body["items"].([]any); len(items) != 0 {
		t.Fatalf("items %v", items)
	}
	if got := e.lookup.last(t); got.Text != "Гиперион Дэн Симмонс" || got.ISBN != "" {
		t.Fatalf("query %+v", got)
	}
}

func TestCandidatesProviderFailure(t *testing.T) {
	e := hyperion(t)
	e.lookup.result = lookup.Result{
		Items:     []lookup.Candidate{{Provider: "openlibrary", Match: "text", Title: "T"}},
		Providers: []lookup.Status{{ID: "fantlab", Status: "error"}, {ID: "openlibrary", Status: "ok"}},
	}
	r := e.admin.do("GET", e.path+"/candidates", nil)
	r.expect(t, "candidates", 200, "")
	p := r.body["providers"].([]any)
	if p[0].(map[string]any)["status"] != "error" || p[1].(map[string]any)["status"] != "ok" {
		t.Fatalf("providers %v", p)
	}
}

func TestMetadataValidation(t *testing.T) {
	e := hyperion(t)
	for name, body := range map[string]map[string]any{
		"title":       {"title": strings.Repeat("я", 501)},
		"authors":     {"authors": strings.Repeat("я", 1001)},
		"series":      {"series": strings.Repeat("я", 501)},
		"language":    {"language": strings.Repeat("я", 17)},
		"description": {"description": strings.Repeat("я", 20001)},
		"index":       {"series": "s", "series_index": -1},
		"source":      {"title": "x", "source": "goodreads"},
		"both covers": {"cover_url": "https://fantlab.ru/a", "reset_cover": true},
		"bad host":    {"cover_url": "https://192.168.1.1/a.jpg"},
		"http":        {"cover_url": "http://fantlab.ru/a.jpg"},
	} {
		e.admin.do("PUT", e.path, body).expect(t, name, 400, "invalid_metadata")
	}
	if len(e.cover.urls) != 0 {
		t.Fatal("forbidden url requested")
	}
	if n := e.count(`SELECT count(*) FROM library_book_meta`); n != 0 {
		t.Fatal("invalid form saved")
	}
	// Предел по символам, а не по байтам, и после обрезки пробелов.
	e.admin.do("PUT", e.path, map[string]any{"title": "  " + strings.Repeat("я", 500) + "  "}).expect(t, "at limit", 200, "")
}

func TestMetadataSaveAndClear(t *testing.T) {
	e := hyperion(t)
	r := e.admin.do("PUT", e.path, map[string]any{
		"title": " Гиперион (ред.) ", "authors": "Дэн Симмонс", "language": "ru", "description": "Аннотация", "source": "fantlab",
	})
	r.expect(t, "put", 200, "")
	book := r.body["book"].(map[string]any)
	if book["title"] != "Гиперион (ред.)" || r.body["description"] != "Аннотация" {
		t.Fatalf("%v", r.body)
	}
	if m := r.body["metadata"].(map[string]any); m["source"] != "fantlab" || m["updated_at"] == nil {
		t.Fatalf("metadata %v", m)
	}
	if f := r.body["files"].([]any)[0].(map[string]any); f["pinned"] != true {
		t.Fatalf("file not pinned: %v", f)
	}

	user := e.panelUser("alice", accounts.RoleUser)
	bookPath := strings.TrimSuffix(e.path, "/metadata")
	if got := user.do("GET", bookPath, nil); got.body["description"] != "Аннотация" {
		t.Fatalf("user view %v", got.body)
	}

	r = e.admin.do("DELETE", e.path, nil)
	r.expect(t, "delete", 200, "")
	if r.body["metadata"] != nil || r.body["description"] != nil || r.body["book"].(map[string]any)["title"] != "Гиперион" {
		t.Fatalf("after delete %v", r.body)
	}
	if f := r.body["files"].([]any)[0].(map[string]any); f["pinned"] != true {
		t.Fatalf("pin removed: %v", f)
	}
}

func TestMetadataCover(t *testing.T) {
	e := hyperion(t)
	const coverURL = "https://covers.openlibrary.org/b/id/1-L.jpg?default=false"
	r := e.admin.do("PUT", e.path, map[string]any{"title": "Гиперион", "authors": "Дэн Симмонс", "language": "ru", "cover_url": coverURL})
	r.expect(t, "put", 200, "")
	cover, _ := r.body["book"].(map[string]any)["cover"].(string)
	if cover == "" || len(e.cover.urls) != 1 || e.cover.urls[0] != coverURL {
		t.Fatalf("cover %q, urls %v", cover, e.cover.urls)
	}
	e.admin.do("GET", "/api/v1/library/covers/"+cover, nil).expect(t, "thumbnail", 200, "")

	r = e.admin.do("PUT", e.path, map[string]any{"title": "Гиперион", "authors": "Дэн Симмонс", "language": "ru"})
	if got := r.body["book"].(map[string]any)["cover"]; got != cover {
		t.Fatalf("cover changed: %v", got)
	}
	r = e.admin.do("PUT", e.path, map[string]any{"title": "Гиперион", "authors": "Дэн Симмонс", "language": "ru", "reset_cover": true})
	if got := r.body["book"].(map[string]any)["cover"]; got != nil {
		t.Fatalf("cover not reset: %v", got)
	}
}

func TestMetadataCoverErrorKeepsEdits(t *testing.T) {
	const coverURL = "https://fantlab.ru/a"
	for name, tt := range map[string]struct {
		data []byte
		err  error
		code string
		want int
	}{
		"недоступна":     {err: fmt.Errorf("%w: status 404", lookup.ErrCoverUnavailable), code: "cover_unavailable", want: 502},
		"не изображение": {data: []byte("html"), code: "cover_unavailable", want: 502},
		"чужой редирект": {err: lookup.ErrCoverURL, code: "invalid_metadata", want: 400},
	} {
		e := hyperion(t)
		e.cover.data, e.cover.err = tt.data, tt.err
		e.admin.do("PUT", e.path, map[string]any{"title": "Новое", "cover_url": coverURL}).expect(t, name, tt.want, tt.code)
		if n := e.count(`SELECT count(*) FROM library_book_meta`); n != 0 {
			t.Fatalf("%s: edit saved", name)
		}
	}
}

func manyAuthorsFB2() string {
	var authors strings.Builder
	for i := range 80 {
		fmt.Fprintf(&authors, "<author><first-name>Имярек</first-name><last-name>Фамилия%d</last-name></author>", i)
	}
	return `<?xml version="1.0" encoding="UTF-8"?><FictionBook><description><title-info>` + authors.String() +
		`<book-title>Антология</book-title><lang>` + strings.Repeat("x", 20) + `</lang><annotation><p>` +
		strings.Repeat("я", 20100) + `</p></annotation></title-info></description></FictionBook>`
}

// Форма присылает все поля: значения файла сверх пределов не должны мешать правке.
func TestMetadataLongFileValues(t *testing.T) {
	e := newMetadataEnv(t, map[string]string{"anthology.fb2": manyAuthorsFB2()}, "anthology.fb2")
	bookPath := strings.TrimSuffix(e.path, "/metadata")
	book := e.admin.do("GET", bookPath, nil).body["book"].(map[string]any)
	authors, language := book["authors"].(string), book["language"].(string)
	if len([]rune(authors)) <= 1000 {
		t.Fatalf("fixture authors are short: %d", len([]rune(authors)))
	}
	r := e.admin.do("PUT", e.path, map[string]any{"title": "Антология", "authors": authors, "language": language, "series": "Новая"})
	r.expect(t, "unchanged long values", 200, "")
	if got := r.body["book"].(map[string]any); got["series"] != "Новая" || got["authors"] != authors {
		t.Fatalf("%v", got)
	}
	e.admin.do("PUT", e.path, map[string]any{"title": "Антология", "authors": authors + "!", "language": language}).
		expect(t, "changed long value", 400, "invalid_metadata")

	first := firstItem(t, e.admin.do("GET", e.path+"/candidates", nil))
	if n := len([]rune(first["description"].(string))); n != 20000 {
		t.Fatalf("file candidate description %d", n)
	}
	if n := len([]rune(first["authors"].(string))); n > 1000 {
		t.Fatalf("file candidate authors %d", n)
	}
}

func TestCandidatesNonLatinLanguageIsNotACode(t *testing.T) {
	fb2 := strings.Replace(metadataFB2, "<lang>ru</lang>", "<lang>рус</lang>", 1)
	e := newMetadataEnv(t, map[string]string{"hyperion.fb2": fb2}, "hyperion.fb2")
	e.admin.do("GET", e.path+"/candidates", nil).expect(t, "candidates", 200, "")
	if got := e.lookup.last(t).Language; got != "" {
		t.Fatalf("language %q", got)
	}
}
