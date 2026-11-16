package lookup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

// stubAPI отдаёт фикстуры по «путь?отсортированный-запрос»; неизвестный запрос — 404.
type stubAPI struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]string // ключ → имя фикстуры или "!500"
	calls  []string
	agents []string
}

func newStub(t *testing.T, routes map[string]string) *stubAPI {
	t.Helper()
	s := &stubAPI{routes: routes}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if q := r.URL.Query().Get("q"); q != "" {
			key += "?" + q
		}
		s.mu.Lock()
		s.calls = append(s.calls, r.URL.String())
		s.agents = append(s.agents, r.UserAgent())
		name, ok := s.routes[key]
		s.mu.Unlock()
		switch {
		case !ok:
			http.NotFound(w, r)
		case strings.HasPrefix(name, "!"):
			var code int
			fmt.Sscanf(name[1:], "%d", &code)
			w.WriteHeader(code)
		case strings.HasPrefix(name, "{") || strings.HasPrefix(name, "["):
			_, _ = w.Write([]byte(name))
		default:
			b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
			if err != nil {
				t.Error(err)
			}
			_, _ = w.Write(b)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stubAPI) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func ctx() context.Context { return context.Background() }

func eq[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}

func TestFantLabISBN(t *testing.T) {
	s := newStub(t, map[string]string{
		"/search-editions?9785170886470":  "fantlab_editions_143403",
		"/edition/143403":                 "fantlab_edition_143403",
		"/search-works?Пикник на обочине": "fantlab_works_piknik",
		"/work/569/extended":              "fantlab_work_569",
	})
	got, err := NewFantLab(s.Client(), s.URL, 0).Search(ctx(), Query{ISBN: "9785170886470"})
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	c := got[0]
	eq(t, "title", c.Title, "Пикник на обочине")
	eq(t, "authors", c.Authors, "Аркадий и Борис Стругацкие")
	eq(t, "isbn", c.ISBN, "9785170886470")
	eq(t, "lang", c.Language, "ru")
	eq(t, "cover", c.CoverURL, "https://fantlab.ru/images/editions/big/143403?r=1660839885")
	eq(t, "year", *c.Year, 2015)
	eq(t, "partial", c.Partial, false)
	if !strings.HasPrefix(c.Description, "Человеческое общество") {
		t.Errorf("description %q", c.Description)
	}
	// Издательская серия и заметка издания не используются, цикла у повести нет.
	eq(t, "series", c.Series, "")
	if strings.Contains(c.Description, "Художник") {
		t.Errorf("edition note leaked: %q", c.Description)
	}
	if !strings.Contains(s.agents[0], "github.com/varrcan/koreader-sync-board") {
		t.Errorf("User-Agent %q", s.agents[0])
	}
}

func TestFantLabISBN10Fallback(t *testing.T) {
	s := newStub(t, map[string]string{
		"/search-editions?9785881963873": "fantlab_editions_empty",
		"/search-editions?5881963873":    "fantlab_editions_184",
		"/edition/184":                   "fantlab_edition_184",
		"/search-works?Гиперион":         "fantlab_works_hyperion",
		"/work/1/extended":               "fantlab_work_1",
	})
	got, err := NewFantLab(s.Client(), s.URL, 0).Search(ctx(), Query{ISBN: "9785881963873"})
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	eq(t, "series", got[0].Series, "Песни Гипериона")
	if got[0].SeriesIndex != nil {
		t.Errorf("series index %v", *got[0].SeriesIndex)
	}
}

func TestFantLabISBNNotFound(t *testing.T) {
	s := newStub(t, map[string]string{"/search-editions?9791234567896": "fantlab_editions_empty"})
	got, err := NewFantLab(s.Client(), s.URL, 0).Search(ctx(), Query{ISBN: "9791234567896"})
	if err != nil || len(got) != 0 {
		t.Fatalf("%+v, %v", got, err)
	}
	eq(t, "calls without ISBN-10 for 979", s.count(), 1)
}

func TestFantLabISBNDetailsFail(t *testing.T) {
	s := newStub(t, map[string]string{
		"/search-editions?9785170886470": "fantlab_editions_143403",
		"/edition/143403":                "!500",
	})
	got, err := NewFantLab(s.Client(), s.URL, 0).Search(ctx(), Query{ISBN: "9785170886470"})
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	eq(t, "partial", got[0].Partial, true)
	eq(t, "title", got[0].Title, "Пикник на обочине")
	eq(t, "authors from BB code", got[0].Authors, "Аркадий и Борис Стругацкие")
}

func TestFantLabText(t *testing.T) {
	s := newStub(t, map[string]string{
		"/search-works?Гиперион": "fantlab_works_hyperion",
		"/work/1/extended":       "fantlab_work_1",
	})
	got, err := NewFantLab(s.Client(), s.URL, 0).Search(ctx(), Query{Text: "Гиперион"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxCandidates {
		t.Fatalf("%d candidates", len(got))
	}
	first := got[0]
	eq(t, "title", first.Title, "Гиперион")
	eq(t, "authors", first.Authors, "Дэн Симмонс")
	eq(t, "language", first.Language, "")
	eq(t, "series", first.Series, "Песни Гипериона")
	eq(t, "cover", first.CoverURL, "https://fantlab.ru/images/editions/big/184?r=1575127844")
	eq(t, "partial", first.Partial, false)
	// Деталей остальных в заглушке нет: кандидат остаётся без них.
	eq(t, "second partial", got[1].Partial, true)
	for _, c := range got {
		if strings.Contains(c.URL, "/work/92") {
			t.Errorf("cycle became a candidate: %+v", c)
		}
	}
}

func TestFantLabFiltersTypesBeforeLimit(t *testing.T) {
	var junk []string
	for i := range 6 {
		junk = append(junk, fmt.Sprintf(`{"work_id":%d,"rusname":"Рецензия","name_show_im":"рецензия"}`, 100+i))
	}
	//nolint:misspell // так поле называет API
	body := "[" + strings.Join(junk, ",") + `,{"work_id":"7","rusname":"[b]Повесть[/b]","name_show_im":"повесть","year":"1972","all_autor_rusname":"[autor=52]Стругацкие[/autor]"}]`
	s := newStub(t, map[string]string{
		"/search-works?x": body,
		//nolint:misspell // так BB-код называет API
		"/work/7/extended": `{"work_description":"[autor=52]Стругацкие[/autor] написали.\r\n[url=http://a]Ссылка[/url]","work_root_saga":[{}]}`,
	})
	got, err := NewFantLab(s.Client(), s.URL, 0).Search(ctx(), Query{Text: "x"})
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	eq(t, "title", got[0].Title, "Повесть")
	eq(t, "authors", got[0].Authors, "Стругацкие")
	eq(t, "description", got[0].Description, "Стругацкие написали.\n\nСсылка")
	eq(t, "year", *got[0].Year, 1972)
	eq(t, "series", got[0].Series, "")
}

func TestFantLabSearchError(t *testing.T) {
	s := newStub(t, map[string]string{"/search-works?x": "!500"})
	if _, err := NewFantLab(s.Client(), s.URL, 0).Search(ctx(), Query{Text: "x"}); err == nil {
		t.Fatal("no error")
	}
}

func TestISBN10(t *testing.T) {
	for in, want := range map[string]string{
		"9785881963873": "5881963873", "9780804429573": "080442957X", "9791234567896": "", "5881963873": "",
	} {
		eq(t, in, isbn10(in), want)
	}
}

func TestOpenLibraryISBN(t *testing.T) {
	s := newStub(t, map[string]string{
		"/search.json?isbn:9780553283686": "ol_search_isbn",
		"/works/OL1963268W.json":          "ol_work_string",
	})
	got, err := NewOpenLibrary(s.Client(), s.URL, 0).Search(ctx(), Query{ISBN: "9780553283686"})
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	c := got[0]
	eq(t, "title", c.Title, "Hyperion")
	eq(t, "authors", c.Authors, "Dan Simmons")
	eq(t, "series", c.Series, "Hyperion Cantos")
	eq(t, "index", *c.SeriesIndex, 1)
	eq(t, "lang", c.Language, "en")
	eq(t, "isbn", c.ISBN, "9780553283686")
	eq(t, "year", *c.Year, 1989)
	eq(t, "cover", c.CoverURL, "https://covers.openlibrary.org/b/id/15211746-L.jpg?default=false")
	if !strings.HasPrefix(c.Description, "In the 29th century") {
		t.Errorf("description %q", c.Description)
	}
}

func TestOpenLibraryTextEdition(t *testing.T) {
	s := newStub(t, map[string]string{
		"/search.json?Гиперион":  "ol_search_text",
		"/works/OL1963268W.json": "ol_work_object",
	})
	got, err := NewOpenLibrary(s.Client(), s.URL, 0).Search(ctx(), Query{Text: "Гиперион", Language: "ru"})
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	eq(t, "title of edition", got[0].Title, "Гиперион")
	eq(t, "lang", got[0].Language, "ru")
	eq(t, "isbn13 preferred", got[0].ISBN, "9785881963873")
	if !strings.HasPrefix(got[0].Description, "Originally published") {
		t.Errorf("object description %q", got[0].Description)
	}
	if !strings.Contains(s.calls[0], "lang=ru") || !strings.Contains(s.calls[0], "limit=3") {
		t.Errorf("request %q", s.calls[0])
	}
}

func TestOpenLibraryDescriptions(t *testing.T) {
	for name, tt := range map[string]struct{ body, want string }{
		"missing":  {`{"title":"x"}`, ""},
		"markdown": {`{"description":"See [Solaris](https://a/b) and [x](y)."}`, "See Solaris and x."},
		"footnote": {`{"description":"[Отзыв в газете][1]: хорошо.\r\n\r\n[1]: https://a/b"}`, "Отзыв в газете: хорошо."},
		"object":   {`{"description":{"type":"/type/text","value":"Текст"}}`, "Текст"},
	} {
		s := newStub(t, map[string]string{
			"/search.json?x":   `{"docs":[{"key":"/works/OL1W","title":"X"}]}`,
			"/works/OL1W.json": tt.body,
		})
		got, err := NewOpenLibrary(s.Client(), s.URL, 0).Search(ctx(), Query{Text: "x"})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: %+v, %v", name, got, err)
		}
		eq(t, name, got[0].Description, tt.want)
		eq(t, name+" partial", got[0].Partial, false)
		eq(t, name+" no cover", got[0].CoverURL, "")
	}
}

func TestOpenLibraryPartialAndLimit(t *testing.T) {
	var docs []string
	for i := range 5 {
		docs = append(docs, fmt.Sprintf(`{"key":"/works/OL%dW","title":"T%d","language":["xxx"]}`, i, i))
	}
	s := newStub(t, map[string]string{"/search.json?x": `{"docs":[` + strings.Join(docs, ",") + `]}`})
	got, err := NewOpenLibrary(s.Client(), s.URL, 0).Search(ctx(), Query{Text: "x"})
	if err != nil || len(got) != openLibraryMax {
		t.Fatalf("%d, %v", len(got), err)
	}
	eq(t, "partial", got[0].Partial, true)
}

func TestClientKeepsGap(t *testing.T) {
	s := newStub(t, map[string]string{"/a": `{}`})
	c := newClient(s.Client(), s.URL, 40*time.Millisecond)
	start := time.Now()
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			var v map[string]any
			if err := c.getJSON(ctx(), "/a", nil, &v); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if d := time.Since(start); d < 80*time.Millisecond {
		t.Fatalf("3 requests in %v", d)
	}
}

func TestClientLimitsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponse+1)))
	}))
	defer srv.Close()
	var v any
	if err := newClient(srv.Client(), srv.URL, 0).getJSON(ctx(), "/", nil, &v); !errors.Is(err, errTooLarge) {
		t.Fatalf("%v", err)
	}
}

func TestGoogle(t *testing.T) {
	s := newStub(t, map[string]string{"/books/v1/volumes?isbn:9780553804577": "google_volumes"})
	got, err := NewGoogle(s.Client(), s.URL, "secret").Search(ctx(), Query{ISBN: "9780553804577"})
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	c := got[0]
	eq(t, "title", c.Title, "The Google Story")
	eq(t, "authors", c.Authors, "David A. Vise, Mark Malseed")
	eq(t, "isbn", c.ISBN, "9780553804577")
	eq(t, "year", *c.Year, 2005)
	eq(t, "lang", c.Language, "en")
	eq(t, "description", c.Description, "Here is the story behind one of the most remarkable Internet successes.\n\nSecond & last.")
	if !strings.HasPrefix(c.CoverURL, "https://books.google.com/") {
		t.Errorf("cover %q", c.CoverURL)
	}
	if !strings.Contains(s.calls[0], "key=secret") || !strings.Contains(s.calls[0], "maxResults=5") || strings.Contains(s.calls[0], "langRestrict") {
		t.Errorf("request %q", s.calls[0])
	}
}

func TestGoogleErrorHidesKey(t *testing.T) {
	s := newStub(t, map[string]string{"/books/v1/volumes?x": "!429"})
	g := NewGoogle(s.Client(), s.URL, "secret")
	_, err := g.Search(ctx(), Query{Text: "x"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("%v", err)
	}
	// Ошибка сети: net/http кладёт полный URL в *url.Error.
	s.Close()
	_, err = g.Search(ctx(), Query{Text: "x"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("%v", err)
	}
}

// fakeProvider считает вызовы и отвечает заранее заданным.
type fakeProvider struct {
	id    string
	calls atomic.Int32
	fn    func(ctx context.Context, q Query) ([]Candidate, error)
}

func (f *fakeProvider) ID() string { return f.id }
func (f *fakeProvider) Search(ctx context.Context, q Query) ([]Candidate, error) {
	f.calls.Add(1)
	return f.fn(ctx, q)
}

func hits(id string, titles ...string) []Candidate {
	out := []Candidate{}
	for _, t := range titles {
		out = append(out, Candidate{Provider: id, Title: t})
	}
	return out
}

func newTestLookup(t *testing.T, providers ...Provider) *Lookup {
	t.Helper()
	db, err := store.Open(ctx(), filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewWithProviders(providers, db, slog.New(slog.DiscardHandler))
}

func describe(r Result) string {
	var parts []string
	for _, c := range r.Items {
		parts = append(parts, c.Provider+":"+c.Match+":"+c.Title)
	}
	return strings.Join(parts, " ")
}

func TestSearchOrderAndFallback(t *testing.T) {
	isbnOnly := func(id string) *fakeProvider {
		return &fakeProvider{id: id, fn: func(_ context.Context, q Query) ([]Candidate, error) {
			if q.ISBN != "" {
				return hits(id, "i"), nil
			}
			t.Error("text search after an ISBN hit")
			return nil, nil
		}}
	}
	textOnly := &fakeProvider{id: ProviderOpenLibrary, fn: func(_ context.Context, q Query) ([]Candidate, error) {
		if q.ISBN != "" {
			return []Candidate{}, nil
		}
		return hits(ProviderOpenLibrary, "t"), nil
	}}
	l := newTestLookup(t, isbnOnly(ProviderFantLab), textOnly, isbnOnly(ProviderGoogle))
	got := l.Search(ctx(), Query{ISBN: "9785170886470", Text: "x"})
	eq(t, "order", describe(got), "fantlab:isbn:i google:isbn:i openlibrary:text:t")
}

func TestSearchBackgroundSkipsGoogle(t *testing.T) {
	answer := func(id string) *fakeProvider {
		return &fakeProvider{id: id, fn: func(context.Context, Query) ([]Candidate, error) { return hits(id, "t"), nil }}
	}
	fantlab, google, ol := answer(ProviderFantLab), answer(ProviderGoogle), answer(ProviderOpenLibrary)
	l := newTestLookup(t, fantlab, google, ol)
	got := l.Search(ctx(), Query{Text: "x", Background: true})
	eq(t, "items", describe(got), "fantlab:text:t openlibrary:text:t")
	eq(t, "providers", fmt.Sprint(got.Providers), "[{fantlab ok} {openlibrary ok}]")
	eq(t, "google calls", google.calls.Load(), 0)

	// Ручной поиск по-прежнему опрашивает Google, остальные отвечают из кеша.
	got = l.Search(ctx(), Query{Text: "x"})
	eq(t, "manual", describe(got), "fantlab:text:t google:text:t openlibrary:text:t")
	eq(t, "google calls", google.calls.Load(), 1)
	eq(t, "fantlab calls", fantlab.calls.Load(), 1)
}

func TestSearchProviderFailure(t *testing.T) {
	bad := &fakeProvider{id: ProviderFantLab, fn: func(context.Context, Query) ([]Candidate, error) {
		return nil, errors.New("boom")
	}}
	good := &fakeProvider{id: ProviderOpenLibrary, fn: func(context.Context, Query) ([]Candidate, error) {
		return hits(ProviderOpenLibrary, "t"), nil
	}}
	got := newTestLookup(t, bad, good).Search(ctx(), Query{Text: "x"})
	eq(t, "items", describe(got), "openlibrary:text:t")
	eq(t, "fantlab", got.Providers[0], Status{ProviderFantLab, StatusError})
	eq(t, "openlibrary", got.Providers[1], Status{ProviderOpenLibrary, StatusOK})
}

func TestSearchTimeoutPerProvider(t *testing.T) {
	stuck := &fakeProvider{id: ProviderFantLab, fn: func(ctx context.Context, _ Query) ([]Candidate, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	l := newTestLookup(t, stuck)
	l.timeout = 20 * time.Millisecond
	eq(t, "status", l.Search(ctx(), Query{Text: "x"}).Providers[0].Status, StatusError)
}

func TestSearchLimitsAndClamps(t *testing.T) {
	long := strings.Repeat("д", maxDescription+10)
	p := &fakeProvider{id: ProviderFantLab, fn: func(context.Context, Query) ([]Candidate, error) {
		out := hits(ProviderFantLab, "1", "2", "3", "4", "5", "6", "7")
		out[0].Description = long
		return out, nil
	}}
	got := newTestLookup(t, p).Search(ctx(), Query{Text: "x"})
	eq(t, "limit", len(got.Items), maxCandidates)
	eq(t, "clamp", len([]rune(got.Items[0].Description)), maxDescription)
}

func TestCache(t *testing.T) {
	p := &fakeProvider{id: ProviderFantLab, fn: func(context.Context, Query) ([]Candidate, error) {
		return hits(ProviderFantLab, "t"), nil
	}}
	l := newTestLookup(t, p)
	now := time.Unix(1_700_000_000, 0)
	l.cache.now = func() time.Time { return now }
	first := l.Search(ctx(), Query{Text: "Ёлка", Language: "ru"})
	// Ключ нормализован: регистр и ё не создают новую запись.
	second := l.Search(ctx(), Query{Text: "ЕЛКА", Language: "ru"})
	eq(t, "same", describe(second), describe(first))
	eq(t, "calls", p.calls.Load(), 1)

	// Другой язык — другой запрос.
	l.Search(ctx(), Query{Text: "Ёлка", Language: "en"})
	eq(t, "language in key", p.calls.Load(), 2)

	now = now.Add(cacheTTL - time.Second)
	l.Search(ctx(), Query{Text: "Ёлка", Language: "ru"})
	eq(t, "not expired", p.calls.Load(), 2)

	now = now.Add(2 * time.Second)
	l.Search(ctx(), Query{Text: "Ёлка", Language: "ru"})
	eq(t, "expired", p.calls.Load(), 3)
}

func TestCacheEmptyResult(t *testing.T) {
	p := &fakeProvider{id: ProviderFantLab, fn: func(context.Context, Query) ([]Candidate, error) {
		return []Candidate{}, nil
	}}
	l := newTestLookup(t, p)
	l.Search(ctx(), Query{ISBN: "9785170886470"})
	l.Search(ctx(), Query{ISBN: "9785170886470"})
	eq(t, "calls", p.calls.Load(), 1)
}

func TestCacheSkipsErrorsPartialAndGoogle(t *testing.T) {
	for name, p := range map[string]*fakeProvider{
		"error": {id: ProviderFantLab, fn: func(context.Context, Query) ([]Candidate, error) { return nil, errors.New("x") }},
		"partial": {id: ProviderOpenLibrary, fn: func(context.Context, Query) ([]Candidate, error) {
			return []Candidate{{Provider: ProviderOpenLibrary, Title: "t", Partial: true}}, nil
		}},
		"google": {id: ProviderGoogle, fn: func(context.Context, Query) ([]Candidate, error) { return hits(ProviderGoogle, "t"), nil }},
	} {
		l := newTestLookup(t, p)
		l.Search(ctx(), Query{Text: "x"})
		l.Search(ctx(), Query{Text: "x"})
		eq(t, name, p.calls.Load(), 2)
	}
}

func TestCachePrunesExpired(t *testing.T) {
	p := &fakeProvider{id: ProviderFantLab, fn: func(context.Context, Query) ([]Candidate, error) { return []Candidate{}, nil }}
	l := newTestLookup(t, p)
	now := time.Unix(1_700_000_000, 0)
	l.cache.now = func() time.Time { return now }
	l.Search(ctx(), Query{Text: "a"})
	now = now.Add(cacheTTL + time.Second)
	l.Search(ctx(), Query{Text: "b"})
	var n int
	if err := l.cache.db.Read.QueryRow(`SELECT count(*) FROM library_metadata_cache`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	eq(t, "rows", n, 1)
}
