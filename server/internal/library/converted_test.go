package library

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

// managed создаёт служебный корень и возвращает его каталог.
func (e *testEnv) managed() string {
	e.t.Helper()
	dir := filepath.Join(e.t.TempDir(), "converted")
	if err := e.st.EnsureManagedRoot(e.ctx, ManagedConverted, dir); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

// convert записывает результат конвертации source в каталог managed и индексирует его.
func (e *testEnv) convert(managed, source, name, content string, b meta.Book) (id int64, path string) {
	e.t.Helper()
	var sum string
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT partial_md5 FROM library_files WHERE path = ?`, source).Scan(&sum); err != nil {
		e.t.Fatalf("%s: %v", source, err)
	}
	rel := sum + "/" + name
	file := filepath.Join(managed, filepath.FromSlash(rel))
	write(e.t, file, content)
	partial, err := hashing.PartialMD5(file)
	if err != nil {
		e.t.Fatal(err)
	}
	id, err = e.st.AddConverted(e.ctx, Converted{
		SourceID: e.fileID(source), SourceMD5: sum, Path: rel, Size: int64(len(content)), MTime: 1,
		PartialMD5: partial, Hashes: append([]string{partial}, hashing.FilenameMD5Variants(name)...), Meta: b,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id, rel
}

// file — файл книги по id.
func (e *testEnv) file(book, id int64) File {
	e.t.Helper()
	d, err := e.st.Book(e.ctx, book, 0, false)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, f := range d.Files {
		if f.ID == id {
			return f
		}
	}
	e.t.Fatalf("file %d not in book %d: %+v", id, book, d.Files)
	return File{}
}

func TestManagedRootHidden(t *testing.T) {
	e := newEnv(t)
	dir := e.managed()
	write(t, filepath.Join(dir, "x/stray.epub"), "stray")
	e.scan()

	if roots, err := e.st.Roots(e.ctx); err != nil || len(roots) != 0 {
		t.Fatalf("roots %+v, %v", roots, err)
	}
	if n := e.count(`SELECT count(*) FROM library_files`); n != 0 {
		t.Fatalf("scanner indexed %d files of the managed root", n)
	}
	sum, err := e.sc.Summary(e.ctx)
	if err != nil || sum.Roots != 0 {
		t.Fatalf("summary %+v, %v", sum, err)
	}
	var id int64
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT id FROM library_roots WHERE managed = 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteRoot(e.ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete managed root: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM library_roots`); n != 1 {
		t.Fatalf("managed root removed, %d left", n)
	}
}

func TestManagedRootOverlap(t *testing.T) {
	e := newEnv(t)
	dir := e.managed()
	for _, p := range []string{filepath.Dir(dir), filepath.Join(dir, "sub")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.AddRoot(e.ctx, p, ""); !errors.Is(err, ErrRootOverlap) {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestManagedRootRelativeDataDir(t *testing.T) {
	e := newEnv(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	if err := e.st.EnsureManagedRoot(e.ctx, ManagedConverted, filepath.Join("data", "converted")); err != nil {
		t.Fatal(err)
	}
	if got := e.strings(`SELECT path FROM library_roots WHERE managed = 1`); got[0] != filepath.Join(base, "data", "converted") {
		t.Fatalf("path %q", got[0])
	}
	if _, err := e.st.AddRoot(e.ctx, filepath.Join(base, "data"), ""); !errors.Is(err, ErrRootOverlap) {
		t.Fatalf("data dir as root: %v", err)
	}
}

func TestManagedRootFollowsDataDir(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	e.addRoot(src)
	write(t, filepath.Join(src, "a.fb2"), "fb2")
	e.book("a.fb2", "A", "X")
	e.scan()
	old := e.managed()
	id, _ := e.convert(old, "a.fb2", "a.epub", "epub", meta.Book{})

	moved := filepath.Join(t.TempDir(), "converted")
	if err := e.st.EnsureManagedRoot(e.ctx, ManagedConverted, moved); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM library_roots WHERE managed = 1`); n != 1 {
		t.Fatalf("%d managed roots", n)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatal(err)
	}
	loc, err := e.st.FileLocation(e.ctx, id)
	if err != nil || loc.Root != moved {
		t.Fatalf("location %+v, %v", loc, err)
	}
}

func TestManagedRootPathTaken(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	if err := e.st.EnsureManagedRoot(e.ctx, ManagedConverted, dir); err == nil {
		t.Fatal("path of a regular root accepted")
	}
	if n := e.count(`SELECT count(*) FROM library_roots WHERE managed = 1`); n != 0 {
		t.Fatalf("%d managed roots", n)
	}
}

func TestAddConverted(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	e.addRoot(src)
	e.book("Пикник.fb2", "Пикник", "Стругацкий")
	e.book("Другой.fb2", "Пикник", "Стругацкий")
	write(t, filepath.Join(src, "Пикник.fb2"), "piknik")
	write(t, filepath.Join(src, "Другой.fb2"), "drugoy")
	e.scan()
	book := e.bookOf("Пикник.fb2")
	source := e.fileID("Пикник.fb2")
	if err := e.st.setCover(e.ctx, source, "cover.jpg"); err != nil {
		t.Fatal(err)
	}

	// Название EPUB другое: по ключу файл попал бы в другую книгу.
	id, rel := e.convert(e.managed(), "Пикник.fb2", "Пикник.epub", "epub", meta.Book{Title: "Иное", Authors: []string{"Некто"}})

	if e.bookOf(rel) != book {
		t.Fatalf("book %d, want %d", e.bookOf(rel), book)
	}
	f := e.file(book, id)
	if !f.Converted || f.Convertible || !f.Pinned || f.Format != "epub" {
		t.Fatalf("converted file %+v", f)
	}
	if s := e.file(book, source); s.Converted || s.Convertible {
		t.Fatalf("source %+v", s)
	}
	if o := e.file(book, e.fileID("Другой.fb2")); o.Converted || !o.Convertible {
		t.Fatalf("other %+v", o)
	}
	if got := e.strings(`SELECT cover FROM library_files WHERE id = ?`, id); got[0] != "cover.jpg" {
		t.Fatalf("cover %q", got[0])
	}
	if n := e.count(`SELECT count(*) FROM library_files WHERE id = ? AND cover_checked = 1`, id); n != 1 {
		t.Fatal("cover_checked not copied")
	}
	d, err := e.st.Book(e.ctx, book, 0, false)
	if err != nil || d.Book.Title == nil || *d.Book.Title != "Пикник" {
		t.Fatalf("book title %+v, %v", d.Book.Title, err)
	}

	// Скан служебный корень не видит и закреплённый файл не трогает.
	e.scan()
	if e.bookOf(rel) != book {
		t.Fatal("scan moved the converted file")
	}
}

func TestAddConvertedSourceGone(t *testing.T) {
	e := newEnv(t)
	e.managed()
	_, err := e.st.AddConverted(e.ctx, Converted{SourceID: 9999, SourceMD5: "x", Path: "x/a.epub"})
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestConvertedSourceLifecycle(t *testing.T) {
	setup := func(t *testing.T) (*testEnv, string, int64, string, string) {
		e := newEnv(t)
		src := t.TempDir()
		e.addRoot(src)
		e.book("a.fb2", "A", "X")
		write(t, filepath.Join(src, "a.fb2"), "first")
		e.scan()
		managed := e.managed()
		id, rel := e.convert(managed, "a.fb2", "a.epub", "epub bytes", meta.Book{})
		return e, src, id, rel, managed
	}

	t.Run("removed", func(t *testing.T) {
		e, src, id, rel, managed := setup(t)
		if err := os.Remove(filepath.Join(src, "a.fb2")); err != nil {
			t.Fatal(err)
		}
		e.scan()
		if f := e.file(e.bookOf(rel), id); !f.Converted {
			t.Fatalf("%+v", f)
		}
		if _, err := os.Stat(filepath.Join(managed, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("renamed", func(t *testing.T) {
		e, src, id, rel, _ := setup(t)
		e.book("b.fb2", "A", "X")
		if err := os.Rename(filepath.Join(src, "a.fb2"), filepath.Join(src, "b.fb2")); err != nil {
			t.Fatal(err)
		}
		e.scan()
		if f := e.file(e.bookOf("b.fb2"), e.fileID("b.fb2")); f.Convertible {
			t.Fatalf("renamed source is convertible: %+v", f)
		}
		if n := e.count(`SELECT count(*) FROM library_files WHERE format = 'epub'`); n != 1 {
			t.Fatalf("%d epub files", n)
		}
		if f := e.file(e.bookOf(rel), id); !f.Converted {
			t.Fatalf("%+v", f)
		}
	})

	t.Run("changed", func(t *testing.T) {
		e, src, id, rel, managed := setup(t)
		before := e.strings(`SELECT hash FROM library_file_hashes WHERE file_id = ? ORDER BY hash`, id)
		write(t, filepath.Join(src, "a.fb2"), "second version")
		e.scan()
		if f := e.file(e.bookOf("a.fb2"), e.fileID("a.fb2")); !f.Convertible {
			t.Fatalf("changed source is not convertible: %+v", f)
		}
		got, err := os.ReadFile(filepath.Join(managed, filepath.FromSlash(rel)))
		if err != nil || string(got) != "epub bytes" {
			t.Fatalf("bytes %q, %v", got, err)
		}
		after := e.strings(`SELECT hash FROM library_file_hashes WHERE file_id = ? ORDER BY hash`, id)
		if len(before) == 0 || len(before) != len(after) || before[0] != after[0] {
			t.Fatalf("hashes %v → %v", before, after)
		}
	})
}

func TestDeleteConverted(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	e.addRoot(src)
	e.book("a.fb2", "A", "X")
	write(t, filepath.Join(src, "a.fb2"), "fb2")
	e.scan()
	managed := e.managed()
	id, rel := e.convert(managed, "a.fb2", "a.epub", "epub", meta.Book{})
	partial := e.strings(`SELECT partial_md5 FROM library_files WHERE id = ?`, id)[0]
	r := e.readers("alice")
	r.sync("alice", partial, 0.4)

	if _, err := e.st.DeleteConverted(e.ctx, e.fileID("a.fb2")); !errors.Is(err, ErrReadOnlyFile) {
		t.Fatalf("regular file: %v", err)
	}
	if _, err := e.st.DeleteConverted(e.ctx, 9999); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	dir, err := e.st.DeleteConverted(e.ctx, id)
	if err != nil || dir != filepath.Dir(filepath.Join(managed, filepath.FromSlash(rel))) {
		t.Fatalf("dir %q, %v", dir, err)
	}
	if n := e.count(`SELECT count(*) FROM library_files WHERE id = ?`, id); n != 0 {
		t.Fatal("file left in the index")
	}
	if n := e.count(`SELECT count(*) FROM library_file_hashes WHERE file_id = ?`, id); n != 0 {
		t.Fatal("hashes left in the index")
	}
	if f := e.file(e.bookOf("a.fb2"), e.fileID("a.fb2")); !f.Convertible {
		t.Fatalf("source %+v", f)
	}
	if n := e.count(`SELECT count(*) FROM progress_current`); n != 1 {
		t.Fatalf("%d progress rows", n)
	}
}

func TestDeleteConvertedRemovesEmptyBook(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	root := e.addRoot(src)
	e.book("a.fb2", "A", "X")
	write(t, filepath.Join(src, "a.fb2"), "fb2")
	e.scan()
	id, _ := e.convert(e.managed(), "a.fb2", "a.epub", "epub", meta.Book{})
	if err := e.st.DeleteRoot(e.ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM library_books`); n != 1 {
		t.Fatalf("%d books after the source root is gone", n)
	}
	if _, err := e.st.DeleteConverted(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM library_books`); n != 0 {
		t.Fatalf("%d books left", n)
	}
}

func TestConvertedSearchIgnoresHashDir(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	e.addRoot(src)
	e.book("a.fb2", "Книга", "Автор")
	write(t, filepath.Join(src, "a.fb2"), "fb2")
	e.scan()
	_, rel := e.convert(e.managed(), "a.fb2", "Особое имя.epub", "epub", meta.Book{})

	e.found("особое", "Книга")
	if got := titles(e.page(rel[:12], 0, 50)); got != "" {
		t.Fatalf("found by hash directory: %q", got)
	}
}

// Прогресс читалки, синхронизированный с partial MD5 результата, виден у файла.
func TestConvertedProgressByPartialMD5(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	e.addRoot(src)
	e.book("a.fb2", "A", "X")
	write(t, filepath.Join(src, "a.fb2"), "fb2")
	e.scan()
	managed := e.managed()
	id, rel := e.convert(managed, "a.fb2", "a.epub", "epub bytes", meta.Book{})

	partial, err := hashing.PartialMD5(filepath.Join(managed, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.strings(`SELECT partial_md5 FROM library_files WHERE id = ?`, id); got[0] != partial {
		t.Fatalf("indexed %q, file %q", got[0], partial)
	}
	r := e.readers("alice")
	r.sync("alice", partial, 0.4)
	d, err := e.st.Book(e.ctx, e.bookOf(rel), r.devices["alice"].UserID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range d.Files {
		if f.ID == id && (f.Progress == nil || f.Progress.Hash != partial || f.Progress.Percentage != 0.4) {
			t.Fatalf("progress %+v", f.Progress)
		}
		if f.ID != id && f.Progress != nil {
			t.Fatalf("source got progress %+v", f.Progress)
		}
	}
}

// CrossPoint в режиме FILENAME хеширует имя как оно есть на устройстве.
func TestConvertedProgressByFilename(t *testing.T) {
	name := "Пикник.epub"
	for variant, sent := range map[string]string{"raw": name, "nfc": norm.NFC.String(name), "nfd": norm.NFD.String(name)} {
		t.Run(variant, func(t *testing.T) {
			e := newEnv(t)
			src := t.TempDir()
			e.addRoot(src)
			e.book("Пикник.fb2", "Пикник", "Стругацкий")
			write(t, filepath.Join(src, "Пикник.fb2"), "fb2")
			e.scan()
			id, rel := e.convert(e.managed(), "Пикник.fb2", name, "epub", meta.Book{})

			sum := md5.Sum([]byte(sent))
			doc := hex.EncodeToString(sum[:])
			r := e.readers("alice")
			r.sync("alice", doc, 0.7)
			d, err := e.st.Book(e.ctx, e.bookOf(rel), r.devices["alice"].UserID, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range d.Files {
				if f.ID == id && (f.Progress == nil || f.Progress.Hash != doc) {
					t.Fatalf("progress %+v, want hash %s", f.Progress, doc)
				}
			}
		})
	}
}
