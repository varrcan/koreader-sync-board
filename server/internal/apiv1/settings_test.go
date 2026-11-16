package apiv1

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/lookup"
	"github.com/varrcan/koreader-sync-board/server/internal/library/suggest"
)

func TestSettings(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	user := e.panelUser("alice", accounts.RoleUser)
	const p = "/api/v1/settings"

	user.do("GET", p, nil).expect(t, "user get", 403, "forbidden")
	user.do("PUT", p, map[string]bool{"library_metadata_suggest": true}).expect(t, "user put", 403, "forbidden")
	r := admin.do("GET", p, nil)
	r.expect(t, "default", 200, "")
	if r.body["library_metadata_suggest"] != false {
		t.Fatalf("default %v", r.body)
	}
	if progress, _ := r.body["metadata_suggestions"].(map[string]any); progress["pending"] != 0.0 || progress["found"] != 0.0 {
		t.Fatalf("progress %v", r.body)
	}

	admin.do("PUT", p, map[string]string{"library_metadata_suggest": "yes"}).expect(t, "bad body", 400, "bad_request")
	r = admin.do("PUT", p, map[string]bool{"library_metadata_suggest": true})
	r.expect(t, "enable", 200, "")
	if r.body["library_metadata_suggest"] != true {
		t.Fatalf("after enable %v", r.body)
	}
	// Отсутствующее поле настройку не меняет.
	r = admin.do("PUT", p, map[string]any{})
	r.expect(t, "empty", 200, "")
	if r.body["library_metadata_suggest"] != true {
		t.Fatalf("empty form changed the setting: %v", r.body)
	}
	if !strings.Contains(e.logs.String(), `"msg":"panel: settings changed","user":"root","library_metadata_suggest":true`) {
		t.Fatalf("no log:\n%s", e.logs)
	}
	if strings.Count(e.logs.String(), "panel: settings changed") != 1 {
		t.Fatal("empty form logged as a change")
	}
}

func TestLibrarySuggestions(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	user := e.panelUser("alice", accounts.RoleUser)
	libraryFixture(t, admin, map[string]string{"a.fb2": metadataFB2, "b.pdf": "b"})
	ids := libraryIDs(t, admin)
	a := ids["a.fb2"]
	if _, err := e.db.Write.ExecContext(e.ctx,
		`INSERT INTO library_book_suggestions (book_id, state, checked_at) VALUES (?, 'found', 0)`, a); err != nil {
		t.Fatal(err)
	}

	mark := func(b *browser, path string) any {
		t.Helper()
		r := b.do("GET", path, nil)
		r.expect(t, "book", 200, "")
		return r.body["book"].(map[string]any)["metadata_suggestion"]
	}
	suggested := func(b *browser) []any {
		t.Helper()
		r := b.do("GET", "/api/v1/library/books?suggested=true", nil)
		r.expect(t, "suggested", 200, "")
		items, _ := r.body["items"].([]any)
		return items
	}
	if items := suggested(admin); len(items) != 1 || items[0].(map[string]any)["metadata_suggestion"] != true {
		t.Fatalf("admin: %v", items)
	}
	if items := suggested(user); len(items) != 0 {
		t.Fatalf("user: %v", items)
	}
	admin.do("GET", "/api/v1/library/books?suggested=maybe", nil).expect(t, "bad flag", 400, "bad_request")
	book := fmt.Sprintf("/api/v1/library/books/%d", a)
	if m := mark(user, book); m != false {
		t.Fatalf("user sees the mark: %v", m)
	}
	if m := mark(admin, book); m != true {
		t.Fatalf("admin: %v", m)
	}

	user.do("DELETE", book+"/metadata/suggestion", nil).expect(t, "user dismiss", 403, "forbidden")
	admin.do("DELETE", "/api/v1/library/books/9999/metadata/suggestion", nil).expect(t, "missing", 404, "not_found")
	admin.do("DELETE", book+"/metadata/suggestion", nil).expect(t, "dismiss", 204, "")
	if m := mark(admin, book); m != false {
		t.Fatalf("after dismiss: %v", m)
	}
	// Без отметки — тот же ответ.
	admin.do("DELETE", fmt.Sprintf("/api/v1/library/books/%d/metadata/suggestion", ids["b.pdf"]), nil).expect(t, "no mark", 204, "")

	// Сохранение правки снимает отметку.
	if _, err := e.db.Write.ExecContext(e.ctx, `UPDATE library_book_suggestions SET state = 'found' WHERE book_id = ?`, a); err != nil {
		t.Fatal(err)
	}
	r := admin.do("PUT", book+"/metadata", map[string]string{"title": "Гиперион", "description": "Аннотация"})
	r.expect(t, "save", 200, "")
	if r.body["book"].(map[string]any)["metadata_suggestion"] != false {
		t.Fatalf("after save: %v", r.body)
	}
	if r := admin.do("GET", "/api/v1/settings", nil); r.body["metadata_suggestions"].(map[string]any)["found"] != 0.0 {
		t.Fatalf("progress: %v", r.body)
	}
}

// fantLab — каталог, который по ISBN Гипериона знает издание с обложкой.
type fantLab struct{ calls atomic.Int32 }

func (*fantLab) ID() string { return lookup.ProviderFantLab }
func (f *fantLab) Search(_ context.Context, q lookup.Query) ([]lookup.Candidate, error) {
	f.calls.Add(1)
	if q.ISBN != "9785170886470" {
		return []lookup.Candidate{}, nil
	}
	return []lookup.Candidate{{Provider: lookup.ProviderFantLab, Title: "Гиперион", CoverURL: "https://fantlab.ru/images/editions/big/1"}}, nil
}

// TestSuggestionsEndToEnd: включение настройки будит фон, книга с ISBN без обложки
// получает отметку, а диалог получает кандидатов из кеша без запросов к каталогу.
func TestSuggestionsEndToEnd(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"hyperion.fb2": metadataFB2})
	id := libraryIDs(t, admin)["hyperion.fb2"]

	fl := &fantLab{}
	lk := lookup.NewWithProviders([]lookup.Provider{fl}, e.db, slog.New(slog.DiscardHandler))
	e.srv.Library.Lookup = lk
	w := suggest.New(e.srv.Library.Store, library.NewFileReader(), lk, e.srv.Settings, slog.New(slog.DiscardHandler))
	e.srv.Settings.OnChange = w.Kick
	ctx, stop := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() { stop(); <-done })

	book := fmt.Sprintf("/api/v1/library/books/%d", id)
	admin.do("PUT", "/api/v1/settings", map[string]bool{"library_metadata_suggest": true}).expect(t, "enable", 200, "")
	deadline := time.Now().Add(5 * time.Second)
	for admin.do("GET", book, nil).body["book"].(map[string]any)["metadata_suggestion"] != true {
		if time.Now().After(deadline) {
			t.Fatal("no suggestion")
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := fl.calls.Load()
	if calls != 1 {
		t.Fatalf("background calls %d", calls)
	}

	r := admin.do("GET", book+"/metadata/candidates", nil)
	r.expect(t, "candidates", 200, "")
	items, _ := r.body["items"].([]any)
	if len(items) != 2 || items[1].(map[string]any)["provider"] != lookup.ProviderFantLab {
		t.Fatalf("candidates %v", items)
	}
	if fl.calls.Load() != calls {
		t.Fatal("dialog queried the catalogue instead of the cache")
	}
}
