package library

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

func (e *testEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Write.ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatal(err)
	}
}

// queue — все книги, ждущие проверки, по порядку.
func (e *testEnv) queue(failedBefore time.Time) []int64 {
	e.t.Helper()
	var out []int64
	var after int64
	for {
		id, err := e.st.NextSuggestCandidate(e.ctx, after, failedBefore)
		if err != nil {
			e.t.Fatal(err)
		}
		if id == 0 {
			return out
		}
		out = append(out, id)
		after = id
	}
}

func (e *testEnv) suggestState(id int64) string {
	e.t.Helper()
	got := e.strings(`SELECT state FROM library_book_suggestions WHERE book_id = ?`, id)
	if len(got) == 0 {
		return "<none>"
	}
	return got[0]
}

func (e *testEnv) record(id int64, state string, metaUpdated int64) {
	e.t.Helper()
	if err := e.st.RecordSuggestCheck(e.ctx, id, state, metaUpdated); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) pending() int {
	e.t.Helper()
	n, err := e.st.SuggestPending(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// suggestBooks индексирует FB2 «A», PDF «B» и PDF без названия.
func suggestBooks(e *testEnv) (a, b, untitled int64) {
	e.t.Helper()
	dir := e.t.TempDir()
	e.addRoot(dir)
	e.book("a.fb2", "A", "Автор")
	e.book("b.pdf", "B", "Автор")
	e.books["scan.pdf"] = meta.Book{}
	for _, n := range []string{"a.fb2", "b.pdf", "scan.pdf"} {
		write(e.t, filepath.Join(dir, n), n)
	}
	e.scan()
	return e.bookOf("a.fb2"), e.bookOf("b.pdf"), e.bookOf("scan.pdf")
}

func TestSuggestQueue(t *testing.T) {
	e := newEnv(t)
	now := time.Unix(1_800_000_000, 0)
	e.st.now = func() time.Time { return now }
	a, b, _ := suggestBooks(e)

	// FB2 ждёт извлечения обложки, PDF обложку не извлекает и не блокирует; книга без
	// названия не отбирается вовсе.
	if got := e.queue(now); len(got) != 1 || got[0] != b {
		t.Fatalf("before covers: %v", got)
	}
	e.exec(`UPDATE library_files SET cover_checked = 1`)
	if got := e.queue(now); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("after covers: %v", got)
	}
	if n := e.pending(); n != 2 {
		t.Fatalf("pending %d", n)
	}

	e.record(a, SuggestFailed, 0)
	e.record(b, SuggestNone, 0)
	if got := e.queue(now.Add(-SuggestRetry)); len(got) != 0 {
		t.Fatalf("fresh failure queued: %v", got)
	}
	if n := e.pending(); n != 0 {
		t.Fatalf("pending %d", n)
	}
	now = now.Add(SuggestRetry)
	if got := e.queue(now.Add(-SuggestRetry)); len(got) != 1 || got[0] != a {
		t.Fatalf("old failure: %v", got)
	}
	if n := e.pending(); n != 1 {
		t.Fatalf("pending %d", n)
	}
	// Повтор после неудачи перезаписывает failed.
	e.record(a, SuggestFound, 0)
	if s := e.suggestState(a); s != SuggestFound {
		t.Fatalf("state %s", s)
	}
	if n, err := e.st.SuggestFound(e.ctx); err != nil || n != 1 {
		t.Fatalf("found %d, %v", n, err)
	}
}

func TestSuggestRecordIsConditional(t *testing.T) {
	e := newEnv(t)
	now := time.Unix(1_800_000_000, 0)
	e.st.now = func() time.Time { return now }
	a, b, _ := suggestBooks(e)

	// Окончательное состояние не перезаписывается.
	e.record(b, SuggestNone, 0)
	e.record(b, SuggestFound, 0)
	if s := e.suggestState(b); s != SuggestNone {
		t.Fatalf("none overwritten: %s", s)
	}

	// Сохранение во время проверки: правка изменилась, итог не пишется.
	e.setMeta(a, MetadataEdit{Title: "A2", Authors: "Автор"})
	before := now.Unix()
	now = now.Add(time.Minute)
	e.setMeta(a, MetadataEdit{Title: "A3", Authors: "Автор"})
	e.record(a, SuggestFound, before)
	if s := e.suggestState(a); s != "<none>" {
		t.Fatalf("stale result written: %s", s)
	}
	// Полный сброс во время проверки — тоже изменение правки.
	e.exec(`UPDATE library_files SET cover_checked = 1`)
	if err := e.st.ClearMetadata(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.record(a, SuggestFound, now.Unix())
	if s := e.suggestState(a); s != "<none>" {
		t.Fatalf("result written after reset: %s", s)
	}
	e.record(a, SuggestFound, 0)
	if s := e.suggestState(a); s != SuggestFound {
		t.Fatalf("state %s", s)
	}

	// Отклонение во время повторной проверки остаётся.
	e.exec(`UPDATE library_book_suggestions SET state = 'failed' WHERE book_id = ?`, b)
	if err := e.st.DismissSuggestion(e.ctx, b); err != nil {
		t.Fatal(err)
	}
	e.record(b, SuggestFound, 0)
	if s := e.suggestState(b); s != SuggestDismissed {
		t.Fatalf("dismissal overwritten: %s", s)
	}

	// Книгу удалили во время проверки.
	e.record(9999, SuggestFound, 0)
	if n := e.count(`SELECT count(*) FROM library_book_suggestions WHERE book_id = 9999`); n != 0 {
		t.Fatal("state for a missing book")
	}
}

func TestSuggestMetadataEdits(t *testing.T) {
	e := newEnv(t)
	a, b, _ := suggestBooks(e)
	e.exec(`UPDATE library_files SET cover_checked = 1`)

	// Правка до проверки: книга остаётся в очереди с исправленным названием.
	e.setMeta(b, MetadataEdit{Title: "B (исправлено)", Authors: "Автор"})
	if s := e.suggestState(b); s != "<none>" {
		t.Fatalf("edit before check: %s", s)
	}
	if got := e.queue(time.Now()); len(got) != 2 {
		t.Fatalf("queue %v", got)
	}

	// Сохранение снимает предложение и убирает книгу из очереди.
	e.record(a, SuggestFound, 0)
	e.setMeta(a, MetadataEdit{Title: "A", Authors: "Автор", Description: "Аннотация"})
	if s := e.suggestState(a); s != SuggestDismissed {
		t.Fatalf("after save: %s", s)
	}
	// Сохранение не трогает другие состояния.
	e.record(b, SuggestNone, e.detail(b).Metadata.UpdatedAt.Unix())
	e.setMeta(b, MetadataEdit{Title: "B", Authors: "Автор"})
	if s := e.suggestState(b); s != SuggestNone {
		t.Fatalf("none changed by save: %s", s)
	}

	// Полный сброс состояние не меняет.
	e.exec(`UPDATE library_book_suggestions SET state = 'found' WHERE book_id = ?`, a)
	if err := e.st.ClearMetadata(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	if s := e.suggestState(a); s != SuggestFound {
		t.Fatalf("after clear: %s", s)
	}
}

func TestSuggestDismissAndCascade(t *testing.T) {
	e := newEnv(t)
	a, _, _ := suggestBooks(e)
	if err := e.st.DismissSuggestion(e.ctx, 9999); !errors.Is(err, ErrBookNotFound) {
		t.Fatalf("missing book: %v", err)
	}
	// Без отметки — то же: книга больше не проверяется.
	if err := e.st.DismissSuggestion(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	if s := e.suggestState(a); s != SuggestDismissed {
		t.Fatalf("state %s", s)
	}
	e.exec(`UPDATE library_book_suggestions SET state = 'found'`)
	if err := e.st.DismissSuggestion(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	if s := e.suggestState(a); s != SuggestDismissed {
		t.Fatalf("state %s", s)
	}

	e.exec(`DELETE FROM library_files WHERE book_id = ?`, a)
	e.exec(`DELETE FROM library_books WHERE id = ?`, a)
	if n := e.count(`SELECT count(*) FROM library_book_suggestions`); n != 0 {
		t.Fatal("state survived the book")
	}
}

func TestCatalogSuggested(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	for _, n := range []string{"a", "b", "c"} {
		e.book(n+".pdf", "Книга "+n, "Автор")
		write(t, filepath.Join(dir, n+".pdf"), n)
	}
	e.book("d.pdf", "Другое", "Автор")
	write(t, filepath.Join(dir, "d.pdf"), "d")
	e.scan()
	for _, n := range []string{"a.pdf", "c.pdf", "d.pdf"} {
		e.record(e.bookOf(n), SuggestFound, 0)
	}
	e.record(e.bookOf("b.pdf"), SuggestNone, 0)

	books := func(f BookFilter, before int64, limit int) BookPage {
		t.Helper()
		p, err := e.st.Books(e.ctx, f, before, limit)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := books(BookFilter{Suggested: true, Admin: true}, 0, 2)
	if titles(p) != "Другое,Книга c" || p.Next == 0 || !p.Items[0].MetadataSuggestion {
		t.Fatalf("first page %q, next %d", titles(p), p.Next)
	}
	if p = books(BookFilter{Suggested: true, Admin: true}, p.Next, 2); titles(p) != "Книга a" || p.Next != 0 {
		t.Fatalf("second page %q", titles(p))
	}
	if p = books(BookFilter{Q: "книга", Suggested: true, Admin: true}, 0, 50); titles(p) != "Книга c,Книга a" {
		t.Fatalf("with q: %q", titles(p))
	}
	all := books(BookFilter{Admin: true}, 0, 50)
	if titles(all) != "Другое,Книга c,Книга b,Книга a" || all.Items[2].MetadataSuggestion || !all.Items[3].MetadataSuggestion {
		t.Fatalf("all: %q %+v", titles(all), all.Items)
	}

	// Отметка не видна пользователю, фильтр даёт пустой список.
	for _, b := range books(BookFilter{}, 0, 50).Items {
		if b.MetadataSuggestion {
			t.Fatalf("user sees the mark on %d", b.ID)
		}
	}
	if p = books(BookFilter{Suggested: true}, 0, 50); len(p.Items) != 0 || p.Items == nil {
		t.Fatalf("user filter: %+v", p)
	}
	if d, err := e.st.Book(e.ctx, e.bookOf("a.pdf"), 0, false); err != nil || d.Book.MetadataSuggestion {
		t.Fatalf("user book: %v", err)
	}
	if !e.detail(e.bookOf("a.pdf")).Book.MetadataSuggestion {
		t.Fatal("admin book without the mark")
	}
}

func TestAutoQuery(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, c := range []struct {
		book       Book
		text, lang string
	}{
		{Book{Title: s("Гиперион"), Authors: s("Дэн Симмонс"), Language: s("ru")}, "Гиперион Дэн Симмонс", "ru"},
		{Book{Title: s("Solaris"), Language: s("EN-us")}, "Solaris", "en"},
		{Book{Filename: "Solaris_1961.pdf", Language: s("рус")}, "Solaris_1961", ""},
		{Book{Filename: "Пикник.fb2.zip", Language: s("r")}, "Пикник", ""},
	} {
		if got := AutoQuery(c.book); got != c.text {
			t.Errorf("AutoQuery %+v: %q", c.book, got)
		}
		if got := BookLanguage(c.book); got != c.lang {
			t.Errorf("BookLanguage %+v: %q", c.book, got)
		}
	}
	long := strings.Repeat("я", 250)
	if got := AutoQuery(Book{Title: &long}); len([]rune(got)) != maxQueryLength {
		t.Fatalf("long title: %d runes", len([]rune(got)))
	}
}
