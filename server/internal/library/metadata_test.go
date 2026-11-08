package library

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

func (e *testEnv) setMeta(id int64, edit MetadataEdit) {
	e.t.Helper()
	if err := e.st.SetMetadata(e.ctx, id, edit); err != nil {
		e.t.Fatal(err)
	}
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// fileForm — форма, в которой все поля совпадают с файлом.
func fileForm() MetadataEdit {
	return MetadataEdit{Title: "Гиперион", Authors: "Дэн Симмонс", Series: "Песни", SeriesIndex: ptrF(1), Language: "ru"}
}

func ptrF(f float64) *float64 { return &f }

// metaBook индексирует одну книгу со всеми полями и возвращает корень и её id.
func metaBook(e *testEnv) (string, int64) {
	e.t.Helper()
	dir := e.t.TempDir()
	e.addRoot(dir)
	e.books["a.epub"] = meta.Book{Title: "Гиперион", Authors: []string{"Дэн Симмонс"}, Series: "Песни", SeriesIndex: ptrF(1), Language: "ru"}
	write(e.t, filepath.Join(dir, "a.epub"), "a")
	e.scan()
	return dir, e.bookOf("a.epub")
}

func TestMetadataSurvivesScanAndKeyChange(t *testing.T) {
	e := newEnv(t)
	dir, id := metaBook(e)
	form := fileForm()
	form.Title = "Гиперион (исправлено)"
	e.setMeta(id, form)

	e.books["a.epub"] = meta.Book{Title: "Совсем другое", Authors: []string{"Другой"}}
	write(t, filepath.Join(dir, "a.epub"), "rewritten")
	e.scan()
	if got := e.bookOf("a.epub"); got != id {
		t.Fatalf("file moved to %d, want %d", got, id)
	}
	if got := str(e.detail(id).Book.Title); got != "Гиперион (исправлено)" {
		t.Fatalf("title %q", got)
	}

	// Ключ остался от файла: новый файл с исходными данными идёт в ту же книгу.
	e.book("b.fb2", "Гиперион", "Дэн Симмонс")
	write(t, filepath.Join(dir, "b.fb2"), "b")
	e.scan()
	if got := e.bookOf("b.fb2"); got != id {
		t.Fatalf("new file in %d, want %d", got, id)
	}
}

func TestMetadataFieldWithoutEditFollowsFile(t *testing.T) {
	e := newEnv(t)
	dir, id := metaBook(e)
	form := fileForm()
	form.Title = "Другое название"
	e.setMeta(id, form)
	if n := e.count(`SELECT count(*) FROM library_book_meta WHERE authors IS NULL AND series IS NULL AND language IS NULL`); n != 1 {
		t.Fatal("field equal to the file is stored")
	}

	e.books["a.epub"] = meta.Book{Title: "Гиперион", Authors: []string{"Новый Автор"}}
	write(t, filepath.Join(dir, "a.epub"), "rewritten")
	e.scan()
	b := e.detail(id).Book
	if str(b.Title) != "Другое название" || str(b.Authors) != "Новый Автор" {
		t.Fatalf("%q / %q", str(b.Title), str(b.Authors))
	}
}

func TestMetadataPDFWithoutTitle(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	write(t, filepath.Join(dir, "scan.pdf"), "p")
	e.scan()
	id := e.bookOf("scan.pdf")

	e.setMeta(id, MetadataEdit{Title: "Солярис"})
	e.found("солярис", "Солярис")

	write(t, filepath.Join(dir, "scan.pdf"), "rewritten")
	e.scan()
	if got := e.bookOf("scan.pdf"); got != id {
		t.Fatalf("file moved to %d", got)
	}
	e.found("солярис", "Солярис")
}

func TestMetadataClearedFieldHidesFile(t *testing.T) {
	e := newEnv(t)
	dir, id := metaBook(e)
	form := fileForm()
	form.Series, form.SeriesIndex, form.Language = "", nil, ""
	e.setMeta(id, form)
	check := func() {
		t.Helper()
		b := e.detail(id).Book
		if b.Series != nil || b.SeriesIndex != nil || b.Language != nil {
			t.Fatalf("%q %v %q", str(b.Series), b.SeriesIndex, str(b.Language))
		}
	}
	check()
	write(t, filepath.Join(dir, "a.epub"), "rewritten")
	e.scan()
	check()
}

func TestMetadataEmptyFieldWithoutFileValue(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.book("a.epub", "Гиперион")
	write(t, filepath.Join(dir, "a.epub"), "a")
	e.scan()
	id := e.bookOf("a.epub")
	e.setMeta(id, MetadataEdit{Title: "Гиперион", Description: "Текст"})
	if n := e.count(`SELECT count(*) FROM library_book_meta WHERE series IS NULL AND authors IS NULL`); n != 1 {
		t.Fatal("empty field stored though the file has no value")
	}
}

func TestMetadataSeriesPair(t *testing.T) {
	for _, tt := range []struct {
		name   string
		series string
		index  *float64
		want   string
		wantI  *float64
	}{
		{"только номер", "Песни", ptrF(5), "Песни", ptrF(5)},
		{"серия без номера", "Другая", nil, "Другая", nil},
		{"номер без серии отброшен", "", ptrF(7), "<nil>", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			_, id := metaBook(e)
			form := fileForm()
			form.Series, form.SeriesIndex = tt.series, tt.index
			e.setMeta(id, form)
			b := e.detail(id).Book
			if str(b.Series) != tt.want {
				t.Fatalf("series %q, want %q", str(b.Series), tt.want)
			}
			if (b.SeriesIndex == nil) != (tt.wantI == nil) || (b.SeriesIndex != nil && *b.SeriesIndex != *tt.wantI) {
				t.Fatalf("index %v, want %v", b.SeriesIndex, tt.wantI)
			}
		})
	}
}

func TestMetadataFormEqualToFileStoresNothing(t *testing.T) {
	e := newEnv(t)
	_, id := metaBook(e)
	e.setMeta(id, fileForm())
	if n := e.count(`SELECT count(*) FROM library_book_meta`); n != 0 {
		t.Fatal("row kept without edits")
	}
	if e.pinned("a.epub") {
		t.Fatal("file pinned without edits")
	}
	if d := e.detail(id); d.Metadata != nil {
		t.Fatalf("metadata %+v", d.Metadata)
	}
}

func TestMetadataDescriptionAlwaysStored(t *testing.T) {
	e := newEnv(t)
	_, id := metaBook(e)
	form := fileForm()
	form.Description = "Аннотация из файла."
	source := "file"
	form.Source = &source
	e.setMeta(id, form)
	d := e.detail(id)
	if str(d.Description) != form.Description {
		t.Fatalf("description %q", str(d.Description))
	}
	if d.Metadata == nil || str(d.Metadata.Source) != "file" || d.Metadata.UpdatedAt.Before(time.Unix(1, 0)) {
		t.Fatalf("metadata %+v", d.Metadata)
	}
	if !e.pinned("a.epub") {
		t.Fatal("file not pinned")
	}
}

func TestMetadataPutReplacesEdits(t *testing.T) {
	e := newEnv(t)
	_, id := metaBook(e)
	form := fileForm()
	form.Title = "Правка"
	form.Description = "Текст"
	e.setMeta(id, form)
	e.setMeta(id, fileForm())
	d := e.detail(id)
	if str(d.Book.Title) != "Гиперион" || d.Description != nil || d.Metadata != nil {
		t.Fatalf("%+v", d)
	}
}

func TestMetadataCoverKeptOrReset(t *testing.T) {
	e := newEnv(t)
	_, id := metaBook(e)
	form := fileForm()
	cover := "abc"
	form.Cover = &cover
	e.setMeta(id, form)
	if got := str(e.detail(id).Book.Cover); got != cover {
		t.Fatalf("cover %q", got)
	}
	e.setMeta(id, fileForm())
	if got := str(e.detail(id).Book.Cover); got != cover {
		t.Fatalf("cover lost: %q", got)
	}
	form = fileForm()
	form.ResetCover = true
	e.setMeta(id, form)
	if got := e.detail(id).Book.Cover; got != nil {
		t.Fatalf("cover not reset: %q", *got)
	}
	if n := e.count(`SELECT count(*) FROM library_book_meta`); n != 0 {
		t.Fatal("empty row kept")
	}
}

func TestMetadataClearKeepsPin(t *testing.T) {
	e := newEnv(t)
	_, id := metaBook(e)
	form := fileForm()
	form.Title = "Правка"
	e.setMeta(id, form)
	if err := e.st.ClearMetadata(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	d := e.detail(id)
	if str(d.Book.Title) != "Гиперион" || d.Metadata != nil {
		t.Fatalf("%+v", d)
	}
	if !e.pinned("a.epub") {
		t.Fatal("clear removed the pin")
	}
}

func TestMetadataMissingBook(t *testing.T) {
	e := newEnv(t)
	if err := e.st.SetMetadata(e.ctx, 9999, MetadataEdit{Title: "x"}); !errors.Is(err, ErrBookNotFound) {
		t.Fatalf("set: %v", err)
	}
	if err := e.st.ClearMetadata(e.ctx, 9999); !errors.Is(err, ErrBookNotFound) {
		t.Fatalf("clear: %v", err)
	}
}

func TestMergeMetadata(t *testing.T) {
	setup := func(t *testing.T) (*testEnv, int64, int64) {
		e := newEnv(t)
		dir := t.TempDir()
		e.addRoot(dir)
		e.book("a.epub", "Первая", "Автор")
		e.book("b.epub", "Вторая", "Автор")
		write(t, filepath.Join(dir, "a.epub"), "a")
		write(t, filepath.Join(dir, "b.epub"), "b")
		e.scan()
		return e, e.bookOf("a.epub"), e.bookOf("b.epub")
	}
	t.Run("правки источника переходят", func(t *testing.T) {
		e, into, from := setup(t)
		e.setMeta(from, MetadataEdit{Title: "Из правки", Authors: "Автор"})
		if err := e.st.Merge(e.ctx, into, from); err != nil {
			t.Fatal(err)
		}
		if got := str(e.detail(into).Book.Title); got != "Из правки" {
			t.Fatalf("title %q", got)
		}
		// С правками закреплены все файлы, и файлы приёмника тоже.
		if !e.pinned("a.epub") || !e.pinned("b.epub") {
			t.Fatal("files not pinned")
		}
		if n := e.count(`SELECT count(*) FROM library_book_meta`); n != 1 {
			t.Fatalf("rows %d", n)
		}
	})
	t.Run("правки приёмника важнее", func(t *testing.T) {
		e, into, from := setup(t)
		e.setMeta(into, MetadataEdit{Title: "Приёмник", Authors: "Автор"})
		e.setMeta(from, MetadataEdit{Title: "Источник", Authors: "Автор"})
		if err := e.st.Merge(e.ctx, into, from); err != nil {
			t.Fatal(err)
		}
		if got := str(e.detail(into).Book.Title); got != "Приёмник" {
			t.Fatalf("title %q", got)
		}
	})
}
