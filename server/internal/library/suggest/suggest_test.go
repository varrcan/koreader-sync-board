package suggest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/lookup"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

func fb2(title, annotation, isbn string) string {
	if annotation != "" {
		annotation = "<annotation><p>" + annotation + "</p></annotation>"
	}
	if isbn != "" {
		isbn = "<publish-info><isbn>" + isbn + "</isbn></publish-info>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?><FictionBook><description><title-info>
		<author><first-name>Дэн</first-name><last-name>Симмонс</last-name></author>
		<book-title>` + title + `</book-title>` + annotation + `<lang>ru</lang></title-info>` + isbn +
		`</description></FictionBook>`
}

type fakeSettings struct{ on atomic.Bool }

func (s *fakeSettings) MetadataSuggest(context.Context) (bool, error) { return s.on.Load(), nil }

// fakeSearch отвечает result и запоминает запросы; onSearch вызывается до ответа.
type fakeSearch struct {
	mu       sync.Mutex
	queries  []lookup.Query
	result   lookup.Result
	onSearch func(ctx context.Context)
}

func (f *fakeSearch) Search(ctx context.Context, q lookup.Query) lookup.Result {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	on, res := f.onSearch, f.result
	f.mu.Unlock()
	if on != nil {
		on(ctx)
	}
	return res
}

func (f *fakeSearch) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

type env struct {
	t        *testing.T
	ctx      context.Context
	db       *store.DB
	st       *library.Store
	dir      string
	scanned  chan struct{}
	scanner  *library.Scanner
	settings *fakeSettings
	search   *fakeSearch
	w        *Worker
}

func ok(providers ...string) []lookup.Status {
	out := []lookup.Status{}
	for _, p := range providers {
		out = append(out, lookup.Status{ID: p, Status: lookup.StatusOK})
	}
	return out
}

// newEnv — библиотека с файлами files (имя → содержимое), уже просканированная и с
// извлечёнными обложками; фоновый поиск включён.
func newEnv(t *testing.T, files map[string]string) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	log := slog.New(slog.DiscardHandler)
	e := &env{t: t, ctx: ctx, db: db, st: library.NewStore(db), dir: t.TempDir(), scanned: make(chan struct{}, 1),
		settings: &fakeSettings{}, search: &fakeSearch{result: lookup.Result{Providers: ok(lookup.ProviderFantLab, lookup.ProviderOpenLibrary)}}}
	e.settings.on.Store(true)
	e.scanner = library.NewScanner(e.st, 0, log, library.WithCovers(library.NewCovers(t.TempDir())),
		library.WithAfterScan(func() { e.scanned <- struct{}{} }))
	scanCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { e.scanner.Run(scanCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	if _, err := e.st.AddRoot(ctx, e.dir, ""); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(e.dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.scan()
	e.w = New(e.st, library.NewFileReader(), e.search, e.settings, log)
	e.w.pause = 0
	return e
}

func (e *env) scan() {
	e.t.Helper()
	e.scanner.Trigger()
	select {
	case <-e.scanned:
	case <-time.After(5 * time.Second):
		e.t.Fatal("scan did not finish")
	}
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Write.ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) book(name string) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT book_id FROM library_files WHERE path = ?`, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) state(name string) string {
	e.t.Helper()
	var s string
	err := e.db.Read.QueryRowContext(e.ctx, `SELECT state FROM library_book_suggestions WHERE book_id = ?`, e.book(name)).Scan(&s)
	if err == sql.ErrNoRows {
		return "<none>"
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) results(items ...lookup.Candidate) {
	e.search.mu.Lock()
	defer e.search.mu.Unlock()
	e.search.result.Items = items
}

const hyperionISBN = "9785170886470"

func TestBookWithoutCover(t *testing.T) {
	e := newEnv(t, map[string]string{"h.fb2": fb2("Гиперион", "Аннотация", "978-5-17-088647-0")})
	// Совпадение по ISBN: название кандидата другое, обложка закрывает пробел.
	e.results(lookup.Candidate{Provider: lookup.ProviderFantLab, Match: lookup.MatchISBN, Title: "Hyperion",
		CoverURL: "https://fantlab.ru/images/editions/big/1"})
	e.w.pass(e.ctx)
	if s := e.state("h.fb2"); s != library.SuggestFound {
		t.Fatalf("state %s", s)
	}
	want := lookup.Query{ISBN: hyperionISBN, Text: "Гиперион Дэн Симмонс", Language: "ru", Background: true}
	if e.search.count() != 1 || e.search.queries[0] != want {
		t.Fatalf("queries %+v", e.search.queries)
	}
	d, err := e.st.Book(e.ctx, e.book("h.fb2"), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if *d.Book.Title != "Гиперион" || d.Book.Cover != nil || d.Metadata != nil || !d.Book.MetadataSuggestion {
		t.Fatalf("book changed: %+v", d)
	}
	// Проверенная книга после нового скана не проверяется.
	e.scan()
	e.w.pass(e.ctx)
	if e.search.count() != 1 {
		t.Fatal("checked book searched again")
	}
}

func TestNoGaps(t *testing.T) {
	e := newEnv(t, map[string]string{"h.fb2": fb2("Гиперион", "Аннотация", "")})
	e.exec(`UPDATE library_files SET cover = 'c'`)
	e.w.pass(e.ctx)
	if s := e.state("h.fb2"); s != library.SuggestComplete || e.search.count() != 0 {
		t.Fatalf("state %s, %d queries", s, e.search.count())
	}
}

func TestUntitledAndDisabled(t *testing.T) {
	e := newEnv(t, map[string]string{"scan.pdf": "not a pdf", "h.fb2": fb2("Гиперион", "", "")})
	e.settings.on.Store(false)
	e.w.pass(e.ctx)
	if e.search.count() != 0 || e.state("h.fb2") != "<none>" {
		t.Fatal("searched with the setting off")
	}
	e.settings.on.Store(true)
	e.w.pass(e.ctx)
	if e.state("scan.pdf") != "<none>" || e.search.count() != 1 || e.search.queries[0].Text != "Гиперион Дэн Симмонс" {
		t.Fatalf("untitled book checked: %+v", e.search.queries)
	}
}

func TestCandidateRules(t *testing.T) {
	cover := "https://covers.openlibrary.org/b/id/1-L.jpg"
	for _, c := range []struct {
		name      string
		hasCover  bool
		providers []lookup.Status
		items     []lookup.Candidate
		want      string
	}{
		{"чужая книга по тексту", false, nil,
			[]lookup.Candidate{{Match: lookup.MatchText, Title: "Гиперион. Сборник", CoverURL: cover}}, library.SuggestNone},
		{"название без учёта регистра", false, nil,
			[]lookup.Candidate{{Match: lookup.MatchText, Title: "ГИПЕРИОН", CoverURL: cover}}, library.SuggestFound},
		{"кандидат не закрывает пробел", true, nil,
			[]lookup.Candidate{{Match: lookup.MatchText, Title: "Гиперион", CoverURL: cover}}, library.SuggestNone},
		{"аннотация закрывает пробел", true, nil,
			[]lookup.Candidate{{Match: lookup.MatchText, Title: "Гиперион", Description: "Текст"}}, library.SuggestFound},
		{"один каталог недоступен",
			false, []lookup.Status{{ID: lookup.ProviderFantLab, Status: lookup.StatusError}, {ID: lookup.ProviderOpenLibrary, Status: lookup.StatusOK}},
			nil, library.SuggestFailed},
		{"кандидат без деталей", false, nil,
			[]lookup.Candidate{{Match: lookup.MatchText, Title: "Гиперион", CoverURL: cover, Partial: true}}, library.SuggestFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, map[string]string{"h.fb2": fb2("Гиперион", "", "")})
			if c.hasCover {
				e.exec(`UPDATE library_files SET cover = 'c'`)
			}
			if c.providers != nil {
				e.search.result.Providers = c.providers
			}
			e.results(c.items...)
			e.w.pass(e.ctx)
			if s := e.state("h.fb2"); s != c.want {
				t.Fatalf("state %s, want %s", s, c.want)
			}
		})
	}
}

func TestFailedRetriedAfterADay(t *testing.T) {
	e := newEnv(t, map[string]string{"h.fb2": fb2("Гиперион", "", "")})
	e.search.result.Providers = []lookup.Status{{ID: lookup.ProviderFantLab, Status: lookup.StatusError}}
	e.w.pass(e.ctx)
	e.w.pass(e.ctx)
	if e.state("h.fb2") != library.SuggestFailed || e.search.count() != 1 {
		t.Fatalf("failed book retried at once: %d queries", e.search.count())
	}
	e.w.now = func() time.Time { return time.Now().Add(library.SuggestRetry + time.Minute) }
	e.search.result.Providers = ok(lookup.ProviderFantLab)
	e.w.pass(e.ctx)
	if e.state("h.fb2") != library.SuggestNone || e.search.count() != 2 {
		t.Fatalf("retry: %s, %d queries", e.state("h.fb2"), e.search.count())
	}
}

func TestRootUnavailable(t *testing.T) {
	e := newEnv(t, map[string]string{"h.fb2": fb2("Гиперион", "", "")})
	if err := os.Rename(e.dir, e.dir+".gone"); err != nil {
		t.Fatal(err)
	}
	e.w.pass(e.ctx)
	if s := e.state("h.fb2"); s != library.SuggestFailed || e.search.count() != 0 {
		t.Fatalf("state %s, %d queries", s, e.search.count())
	}
}

// run запускает Run и возвращает остановку, которая ждёт выхода.
func (e *env) run() func() {
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.w.Run(ctx); close(done) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			e.t.Fatal("Run did not stop")
		}
	}
}

func (e *env) waitState(name, want string) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.state(name) != want {
		if time.Now().After(deadline) {
			e.t.Fatalf("state %s, want %s", e.state(name), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Без периодического скана сигнала о его конце нет: Run проверяет книги сам.
func TestRunStartsWithoutScan(t *testing.T) {
	e := newEnv(t, map[string]string{"h.fb2": fb2("Гиперион", "", "")})
	stop := e.run()
	defer stop()
	e.waitState("h.fb2", library.SuggestNone)

	// Kick после нового скана берёт новую книгу.
	if err := os.WriteFile(filepath.Join(e.dir, "s.fb2"), []byte(fb2("Солярис", "", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	e.scan()
	e.w.Kick()
	e.waitState("s.fb2", library.SuggestNone)
}

func TestStopDuringSearch(t *testing.T) {
	e := newEnv(t, map[string]string{"h.fb2": fb2("Гиперион", "", "")})
	entered := make(chan struct{})
	e.search.onSearch = func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
	}
	e.search.result.Providers = []lookup.Status{{ID: lookup.ProviderFantLab, Status: lookup.StatusError}}
	stop := e.run()
	<-entered
	stop()
	if s := e.state("h.fb2"); s != "<none>" {
		t.Fatalf("interrupted check recorded: %s", s)
	}
}

func TestDisableMidQueue(t *testing.T) {
	files := map[string]string{}
	for i := range 3 {
		files[fmt.Sprintf("%d.fb2", i)] = fb2(fmt.Sprintf("Книга %d", i), "", "")
	}
	e := newEnv(t, files)
	e.search.onSearch = func(context.Context) { e.settings.on.Store(false) }
	e.w.pass(e.ctx)
	if n := e.search.count(); n != 1 {
		t.Fatalf("%d books searched after disabling", n)
	}
	var checked int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM library_book_suggestions`).Scan(&checked); err != nil || checked != 1 {
		t.Fatalf("checked %d, %v", checked, err)
	}
}

// Пауза между книгами с запросами; книги без запросов идут без неё.
func TestPauseBetweenSearches(t *testing.T) {
	e := newEnv(t, map[string]string{"a.fb2": fb2("А", "", ""), "b.fb2": fb2("Б", "", ""), "c.fb2": fb2("В", "Есть", "")})
	e.exec(`UPDATE library_files SET cover = 'c' WHERE path = 'c.fb2'`)
	e.w.pause = 50 * time.Millisecond
	start := time.Now()
	e.w.pass(e.ctx)
	if d := time.Since(start); d < 100*time.Millisecond || d > 140*time.Millisecond+time.Second {
		t.Fatalf("pass took %v", d)
	}
	if e.search.count() != 2 || e.state("c.fb2") != library.SuggestComplete {
		t.Fatal("unexpected checks")
	}
}
