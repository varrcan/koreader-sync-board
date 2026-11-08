package library

import (
	"database/sql"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
)

func TestFoldFunctionInConnections(t *testing.T) {
	e := newEnv(t)
	for name, db := range map[string]*sql.DB{"read": e.db.Read, "write": e.db.Write} {
		var got string
		if err := db.QueryRowContext(e.ctx, `SELECT ksb_fold('Ёлка, ПАЛКА')`).Scan(&got); err != nil || got != "елка палка" {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	var got string
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT ksb_fold(NULL)`).Scan(&got); err != nil || got != "" {
		t.Errorf("NULL: %q, %v", got, err)
	}
}

// book задаёт метаданные файла name для подменного разбора.
func (e *testEnv) book(name, title string, authors ...string) {
	e.books[name] = meta.Book{Title: title, Authors: authors}
}

func (e *testEnv) page(q string, before int64, limit int) BookPage {
	e.t.Helper()
	p, err := e.st.Books(e.ctx, q, before, limit)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func titles(p BookPage) string {
	out := []string{}
	for _, b := range p.Items {
		if b.Title != nil {
			out = append(out, *b.Title)
		} else {
			out = append(out, "<"+b.Filename+">")
		}
	}
	return strings.Join(out, ",")
}

func (e *testEnv) found(q, want string) {
	e.t.Helper()
	if got := titles(e.page(q, 0, 50)); got != want {
		e.t.Errorf("q=%q: %q, want %q", q, got, want)
	}
}

func TestCatalogOrderAndPages(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	for _, n := range []string{"a", "b", "c"} {
		e.book(n+".epub", strings.ToUpper(n), "Автор")
		write(t, filepath.Join(dir, n+".epub"), n)
		e.scan()
	}
	p := e.page("", 0, 2)
	if titles(p) != "C,B" || p.Next == 0 {
		t.Fatalf("first page %q, next %d", titles(p), p.Next)
	}
	// Книга, созданная во время листания, остаётся выше курсора.
	e.book("d.epub", "D", "Автор")
	write(t, filepath.Join(dir, "d.epub"), "d")
	e.scan()
	p = e.page("", p.Next, 2)
	if titles(p) != "A" || p.Next != 0 {
		t.Fatalf("second page %q, next %d", titles(p), p.Next)
	}
	if p := e.page("", 0, 4); p.Next != 0 || titles(p) != "D,C,B,A" {
		t.Fatalf("exact page %q, next %d", titles(p), p.Next)
	}
}

func TestCatalogSearch(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.books["piknik.epub"] = meta.Book{Title: "Пикник на обочине",
		Authors: []string{"Аркадий Стругацкий", "Борис Стругацкий"}, Series: "Мир Полудня"}
	e.book("elka.fb2", "Ёлка", "Зощенко")
	write(t, filepath.Join(dir, "piknik.epub"), "1")
	write(t, filepath.Join(dir, "elka.fb2"), "2")
	write(t, filepath.Join(dir, "scans/Тайга.pdf"), "3")
	e.scan()

	e.found("СТРУГАЦКИЙ пикник", "Пикник на обочине")
	e.found("полудня", "Пикник на обочине")
	e.found("елка", "Ёлка")
	e.found("ЁЛКА!", "Ёлка")
	e.found("тайга", "<Тайга.pdf>")
	e.found("scans", "<Тайга.pdf>")
	e.found("стругацкий ёлка", "")
	e.found(" ... ", "<Тайга.pdf>,Пикник на обочине,Ёлка")
}

func TestCatalogFiles(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.book("piknik.fb2", "Пикник", "Стругацкий")
	e.book("Piknik.epub", "Пикник.", "Стругацкий")
	e.book("copy.epub", "Пикник", "Стругацкий")
	write(t, filepath.Join(dir, "a/piknik.fb2"), "fb2")
	e.scan()
	write(t, filepath.Join(dir, "b/Piknik.epub"), "epub")
	write(t, filepath.Join(dir, "c/copy.epub"), "copy")
	e.scan()
	p := e.page("", 0, 50)
	if len(p.Items) != 1 {
		t.Fatalf("books %+v", p.Items)
	}
	b := p.Items[0]
	if b.Files != 3 || !reflect.DeepEqual(b.Formats, []string{"fb2", "epub"}) || b.Filename != "piknik.fb2" {
		t.Fatalf("book %+v", b)
	}
}

type reader struct {
	e       *testEnv
	acc     *accounts.Service
	devices map[string]accounts.Principal
	now     time.Time
}

func (e *testEnv) readers(names ...string) *reader {
	e.t.Helper()
	acc := accounts.New(e.db, accounts.WithArgon2Params(accounts.TestArgon2Params))
	r := &reader{e: e, acc: acc, devices: map[string]accounts.Principal{}, now: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	for _, n := range names {
		if _, err := acc.CreateUser(e.ctx, n, accounts.RoleUser); err != nil {
			e.t.Fatal(err)
		}
		_, pass, err := acc.AddDevice(e.ctx, n, "dev")
		if err != nil {
			e.t.Fatal(err)
		}
		p, err := acc.Authenticate(e.ctx, n, accounts.KeyFromPassword(pass), netip.Addr{})
		if err != nil {
			e.t.Fatal(err)
		}
		r.devices[n] = p
	}
	return r
}

func (r *reader) sync(user, doc string, pct float64) {
	r.e.t.Helper()
	r.now = r.now.Add(time.Minute)
	if err := progress.New(r.e.db).Save(r.e.ctx, r.devices[user], progress.Update{
		Document: doc, Percentage: pct, Progress: "x", Device: "d", ReceivedAt: r.now}); err != nil {
		r.e.t.Fatal(err)
	}
}

func TestBookDetail(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	root := e.addRoot(dir)
	e.book("piknik.fb2", "Пикник", "Стругацкий")
	e.book("Piknik.epub", "Пикник", "Стругацкий")
	epub := filepath.Join(dir, "sub/Piknik.epub")
	write(t, filepath.Join(dir, "piknik.fb2"), "fb2 content")
	e.scan()
	write(t, epub, "epub content")
	e.scan()
	partial, err := hashing.PartialMD5(epub)
	if err != nil {
		t.Fatal(err)
	}
	byName := hashing.FilenameMD5Variants("Piknik.epub")[0]

	r := e.readers("alice", "bob")
	aliceID, bobID := r.devices["alice"].UserID, r.devices["bob"].UserID
	r.sync("alice", partial, 0.4)
	r.sync("bob", partial, 0.9)
	id := e.bookOf("piknik.fb2")

	d, err := e.st.Book(e.ctx, id, aliceID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Book.ID != id || d.Book.Files != 2 || len(d.Files) != 2 {
		t.Fatalf("detail %+v", d)
	}
	fb2, ep := d.Files[0], d.Files[1]
	if fb2.Path != "piknik.fb2" || fb2.Progress != nil || fb2.RootName != filepath.Base(dir) ||
		fb2.Format != "fb2" || fb2.Size != int64(len("fb2 content")) || fb2.Pinned {
		t.Fatalf("fb2 %+v", fb2)
	}
	if ep.Path != "sub/Piknik.epub" || ep.Progress == nil || ep.Progress.Percentage != 0.4 ||
		ep.Progress.Hash != partial || ep.Progress.Finished {
		t.Fatalf("epub %+v %+v", ep, ep.Progress)
	}

	// Последний по времени из хешей файла — здесь CrossPoint с md5 имени.
	r.sync("alice", byName, 0.5)
	d, _ = e.st.Book(e.ctx, id, aliceID)
	if p := d.Files[1].Progress; p == nil || p.Hash != byName || p.Percentage != 0.5 {
		t.Fatalf("by name %+v", p)
	}
	r.sync("alice", partial, 1)
	d, _ = e.st.Book(e.ctx, id, aliceID)
	if p := d.Files[1].Progress; p == nil || p.Hash != partial || !p.Finished {
		t.Fatalf("latest %+v", p)
	}

	d, _ = e.st.Book(e.ctx, id, bobID)
	if p := d.Files[1].Progress; p == nil || p.Percentage != 0.9 {
		t.Fatalf("bob %+v", p)
	}
	if _, err := e.st.Book(e.ctx, 9999, aliceID); !errors.Is(err, ErrBookNotFound) {
		t.Fatalf("missing book: %v", err)
	}

	loc, err := e.st.FileLocation(e.ctx, d.Files[1].ID)
	if err != nil || loc != (FileLocation{Root: root.Path, Path: "sub/Piknik.epub", Format: "epub"}) {
		t.Fatalf("location %+v %v", loc, err)
	}
	if _, err := e.st.FileLocation(e.ctx, 9999); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing file: %v", err)
	}
}

func (e *testEnv) detail(id int64) BookDetail {
	e.t.Helper()
	d, err := e.st.Book(e.ctx, id, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *testEnv) fileID(path string) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT id FROM library_files WHERE path = ?`, path).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *testEnv) pinned(path string) bool {
	e.t.Helper()
	return e.count(`SELECT pinned FROM library_files WHERE path = ?`, path) == 1
}

func TestPinnedFileKeepsBook(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.book("a.epub", "Первая", "Автор")
	e.book("b.epub", "Вторая", "Автор")
	write(t, filepath.Join(dir, "a.epub"), "a")
	write(t, filepath.Join(dir, "b.epub"), "b")
	e.scan()
	first, second := e.bookOf("a.epub"), e.bookOf("b.epub")
	if _, err := e.db.Write.ExecContext(e.ctx, `UPDATE library_files SET pinned = 1 WHERE path = 'a.epub'`); err != nil {
		t.Fatal(err)
	}

	// Новый ключ принадлежит другой книге, но книгу файла задал человек.
	e.book("a.epub", "Вторая", "Автор")
	write(t, filepath.Join(dir, "a.epub"), "a changed")
	partial, err := hashing.PartialMD5(filepath.Join(dir, "a.epub"))
	if err != nil {
		t.Fatal(err)
	}
	e.scan()
	if got := e.bookOf("a.epub"); got != first {
		t.Fatalf("pinned file moved to %d, want %d", got, first)
	}
	d := e.detail(first)
	if *d.Book.Title != "Вторая" || *d.Files[0].Title != "Вторая" || !d.Files[0].Pinned {
		t.Fatalf("metadata not updated: %+v", d)
	}
	if n := e.count(`SELECT count(*) FROM library_file_hashes WHERE hash = ?`, partial); n != 1 {
		t.Fatalf("hash not updated: %d", n)
	}

	// Удалён и найден снова — это новый файл, распределяется по ключу.
	if err := os.Remove(filepath.Join(dir, "a.epub")); err != nil {
		t.Fatal(err)
	}
	e.scan()
	write(t, filepath.Join(dir, "a.epub"), "a again")
	e.scan()
	if got := e.bookOf("a.epub"); got != second || e.pinned("a.epub") {
		t.Fatalf("after re-add: book %d (want %d), pinned %v", got, second, e.pinned("a.epub"))
	}
	if n := e.count(`SELECT count(*) FROM library_books WHERE id = ?`, first); n != 0 {
		t.Fatalf("empty book left")
	}
}

func TestMerge(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.book("a.epub", "Пикник", "Стругацкий")
	e.book("b.fb2", "Пикник (сборник)", "Стругацкий")
	write(t, filepath.Join(dir, "a.epub"), "a")
	write(t, filepath.Join(dir, "b.fb2"), "b")
	write(t, filepath.Join(dir, "nameless.pdf"), "n")
	e.scan()
	into, from, nameless := e.bookOf("a.epub"), e.bookOf("b.fb2"), e.bookOf("nameless.pdf")

	if err := e.st.Merge(e.ctx, into, into); !errors.Is(err, ErrSameBook) {
		t.Fatalf("self merge: %v", err)
	}
	for _, c := range [][2]int64{{9999, from}, {into, 9999}} {
		if err := e.st.Merge(e.ctx, c[0], c[1]); !errors.Is(err, ErrBookNotFound) {
			t.Fatalf("missing book %v: %v", c, err)
		}
	}

	if err := e.st.Merge(e.ctx, into, from); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM library_books WHERE id = ?`, from); n != 0 {
		t.Fatal("source not deleted")
	}
	if n := e.count(`SELECT count(*) FROM library_book_keys WHERE book_id = ?`, into); n != 2 {
		t.Fatalf("keys %d", n)
	}
	d := e.detail(into)
	if len(d.Files) != 2 || *d.Book.Title != "Пикник" || d.Files[0].Pinned || !d.Files[1].Pinned {
		t.Fatalf("after merge %+v", d)
	}

	// Новый файл по ключу источника идёт в цель.
	e.book("c.epub", "Пикник (сборник)", "Стругацкий")
	write(t, filepath.Join(dir, "c.epub"), "c")
	e.scan()
	if got := e.bookOf("c.epub"); got != into {
		t.Fatalf("new file in %d, want %d", got, into)
	}

	// У книги без названия ключа нет, файл удерживает закрепление.
	if err := e.st.Merge(e.ctx, into, nameless); err != nil {
		t.Fatal(err)
	}
	e.book("nameless.pdf", "Совсем другое", "Некто")
	write(t, filepath.Join(dir, "nameless.pdf"), "named now")
	e.scan()
	if got := e.bookOf("nameless.pdf"); got != into {
		t.Fatalf("changed file in %d, want %d", got, into)
	}
	if n := e.count(`SELECT count(*) FROM library_books`); n != 1 {
		t.Fatalf("books %d", n)
	}
}

func TestSplit(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.book("a.epub", "Сборник", "Автор")
	e.book("b.epub", "Сборник", "Автор")
	write(t, filepath.Join(dir, "a.epub"), "a")
	write(t, filepath.Join(dir, "b.epub"), "b")
	e.scan()
	old := e.bookOf("a.epub")

	if _, err := e.st.Split(e.ctx, 9999); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	id, err := e.st.Split(e.ctx, e.fileID("b.epub"))
	if err != nil {
		t.Fatal(err)
	}
	if id == old || e.bookOf("b.epub") != id || !e.pinned("b.epub") {
		t.Fatalf("split: new %d old %d book %d", id, old, e.bookOf("b.epub"))
	}
	if n := e.count(`SELECT count(*) FROM library_book_keys WHERE book_id = ?`, id); n != 0 {
		t.Fatalf("new book has %d keys", n)
	}
	if d := e.detail(id); len(d.Files) != 1 || *d.Book.Title != "Сборник" {
		t.Fatalf("new book %+v", d)
	}
	if d := e.detail(old); len(d.Files) != 1 {
		t.Fatalf("old book %+v", d)
	}
	if _, err := e.st.Split(e.ctx, e.fileID("a.epub")); !errors.Is(err, ErrSingleFile) {
		t.Fatalf("single file: %v", err)
	}

	// Перезапись с теми же метаданными не возвращает файл в прежнюю книгу.
	write(t, filepath.Join(dir, "b.epub"), "b overwritten")
	e.scan()
	if got := e.bookOf("b.epub"); got != id {
		t.Fatalf("file went back to %d", got)
	}
}

func TestDeleteRootRefreshesBooks(t *testing.T) {
	e := newEnv(t)
	first, second := t.TempDir(), t.TempDir()
	r := e.addRoot(first)
	e.addRoot(second)
	e.book("a.epub", "Первая", "Автор")
	e.book("b.epub", "Вторая", "Автор")
	e.book("c.epub", "Третья", "Автор")
	write(t, filepath.Join(first, "a.epub"), "a")
	write(t, filepath.Join(first, "c.epub"), "c")
	write(t, filepath.Join(second, "b.epub"), "b")
	e.scan()
	into, gone := e.bookOf("a.epub"), e.bookOf("c.epub")
	if err := e.st.Merge(e.ctx, into, e.bookOf("b.epub")); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteRoot(e.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if d := e.detail(into); *d.Book.Title != "Вторая" || d.Book.Files != 1 {
		t.Fatalf("after delete %+v", d.Book)
	}
	if n := e.count(`SELECT count(*) FROM library_books WHERE id = ?`, gone); n != 0 {
		t.Fatal("book without files left")
	}
	if err := e.st.DeleteRoot(e.ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
