package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const annotatedFB2 = `<?xml version="1.0" encoding="UTF-8"?><FictionBook><description><title-info>
	<book-title>Гиперион</book-title><annotation><p>Первый.</p><p>Второй.</p></annotation></title-info>
	<publish-info><isbn>978-5-17-088647-0</isbn></publish-info></description></FictionBook>`

func TestFileReaderMergesFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.fb2"), annotatedFB2)
	write(t, filepath.Join(root, "broken.epub"), "not a zip")
	got := NewFileReader().Read([]FileLocation{
		{Root: root, Path: "missing.fb2", Format: "fb2"},
		{Root: root, Path: "broken.epub", Format: "epub"},
		{Root: root, Path: "a.fb2", Format: "fb2"},
	})
	if got.Title != "Гиперион" || got.ISBN != "9785170886470" || got.Description != "Первый.\n\nВторой." {
		t.Fatalf("%+v", got)
	}
}

func TestFileReaderSymlinkOutsideRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(outside, "secret.fb2"), annotatedFB2)
	if err := os.Symlink(filepath.Join(outside, "secret.fb2"), filepath.Join(root, "a.fb2")); err != nil {
		t.Fatal(err)
	}
	got := NewFileReader().Read([]FileLocation{{Root: root, Path: "a.fb2", Format: "fb2"}})
	if got.Title != "" || got.ISBN != "" {
		t.Fatalf("symlink was parsed: %+v", got)
	}
}

func TestFileReaderHungRead(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	r := FileReader{Budget: 30 * time.Millisecond, Open: func(string, string) (*os.File, error) {
		<-release
		return nil, os.ErrClosed
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if got := r.Read([]FileLocation{{Root: "/", Path: "a.fb2", Format: "fb2"}}); got.Title != "" {
			t.Errorf("%+v", got)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not honour the budget")
	}
}

func TestParseableFiles(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	e.book("a.pdf", "Книга", "Автор")
	e.book("b.epub", "Книга", "Автор")
	write(t, filepath.Join(dir, "a.pdf"), "a")
	write(t, filepath.Join(dir, "b.epub"), "b")
	e.scan()
	got, err := e.st.ParseableFiles(e.ctx, e.bookOf("a.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "b.epub" || got[0].Root != dir {
		t.Fatalf("%+v", got)
	}
}
