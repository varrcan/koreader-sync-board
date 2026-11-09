package library

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func (e *testEnv) feed(q string, before int64, limit int) FeedPage {
	e.t.Helper()
	p, err := e.st.Feed(e.ctx, q, before, limit)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func TestFeedFiles(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.book("a.fb2", "Пикник", "Стругацкий")
	e.book("a.epub", "Пикник", "Стругацкий")
	e.book("other.pdf", "Другая", "Автор")
	write(t, filepath.Join(dir, "a.fb2"), "fb2 file")
	write(t, filepath.Join(dir, "sub/a.epub"), "epub")
	e.scan()
	write(t, filepath.Join(dir, "other.pdf"), "pdf")
	e.scan()

	p := e.feed("", 0, 10)
	if len(p.Items) != 2 || p.Next != 0 {
		t.Fatalf("page %+v", p)
	}
	other, book := p.Items[0], p.Items[1]
	if other.Book.Title == nil || *other.Book.Title != "Другая" || len(other.Files) != 1 || other.Files[0].Format != "pdf" {
		t.Fatalf("newest %+v", other)
	}
	if len(book.Files) != 2 {
		t.Fatalf("files %+v", book.Files)
	}
	for i, want := range []FeedFile{{Path: "a.fb2", Format: "fb2", Size: 8}, {Path: "sub/a.epub", Format: "epub", Size: 4}} {
		got := book.Files[i]
		if got.ID == 0 {
			t.Fatalf("file %d has no id", i)
		}
		got.ID = 0
		if got != want {
			t.Fatalf("file %d: %+v, want %+v", i, got, want)
		}
	}
	if book.Book.Filename != "a.fb2" || !reflect.DeepEqual(book.Book.Formats, []string{"fb2", "epub"}) {
		t.Fatalf("book %+v", book.Book)
	}
}

func TestFeedDescriptionAndUpdated(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.book("a.epub", "A", "Автор")
	write(t, filepath.Join(dir, "a.epub"), "a")
	e.scan()

	var created int64
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT created_at FROM library_books`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	p := e.feed("", 0, 10)
	b := p.Items[0]
	if b.Description != nil || !b.Updated.Equal(time.Unix(created, 0)) || b.Updated.Location() != time.UTC {
		t.Fatalf("without edits: %+v", b)
	}

	if err := e.st.SetMetadata(e.ctx, b.Book.ID, MetadataEdit{Title: "A", Authors: "Автор", Description: "Описание"}); err != nil {
		t.Fatal(err)
	}
	const edited = 2_000_000_000
	if _, err := e.db.Write.ExecContext(e.ctx, `UPDATE library_book_meta SET updated_at = ?`, edited); err != nil {
		t.Fatal(err)
	}
	b = e.feed("", 0, 10).Items[0]
	if b.Description == nil || *b.Description != "Описание" || !b.Updated.Equal(time.Unix(edited, 0)) {
		t.Fatalf("with edits: %+v", b)
	}
}

func TestFeedPagesAndSearch(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	for _, n := range []string{"a", "b", "c"} {
		e.book(n+".epub", "Книга "+n, "Автор")
		write(t, filepath.Join(dir, n+".epub"), n)
		e.scan()
	}
	e.book("elka.fb2", "Ёлка", "Зощенко")
	write(t, filepath.Join(dir, "elka.fb2"), "e")
	e.scan()

	first := e.feed("книга", 0, 2)
	if len(first.Items) != 2 || first.Next == 0 || *first.Items[0].Book.Title != "Книга c" {
		t.Fatalf("first %+v", first)
	}
	second := e.feed("книга", first.Next, 2)
	if len(second.Items) != 1 || second.Next != 0 || *second.Items[0].Book.Title != "Книга a" {
		t.Fatalf("second %+v", second)
	}
	if p := e.feed("елка", 0, 2); len(p.Items) != 1 || len(p.Items[0].Files) != 1 {
		t.Fatalf("fold %+v", p)
	}
	if p := e.feed("нет такой", 0, 2); len(p.Items) != 0 || p.Next != 0 {
		t.Fatalf("nothing %+v", p)
	}
}

// Слияние между чтениями страницы не должно оставить книгу без файлов.
func TestFeedSnapshot(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.book("a.epub", "A", "Автор")
	e.book("b.epub", "B", "Автор")
	write(t, filepath.Join(dir, "a.epub"), "a")
	e.scan()
	write(t, filepath.Join(dir, "b.epub"), "b")
	e.scan()

	tx, err := e.db.Read.BeginTx(e.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before, err := feedPage(e.ctx, tx, "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.Merge(e.ctx, before.Items[1].Book.ID, before.Items[0].Book.ID); err != nil {
		t.Fatal(err)
	}
	same, err := feedPage(e.ctx, tx, "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, same) {
		t.Fatalf("snapshot changed:\n%+v\n%+v", before, same)
	}
	if p := e.feed("", 0, 10); len(p.Items) != 1 || len(p.Items[0].Files) != 2 {
		t.Fatalf("after merge %+v", p)
	}
}
