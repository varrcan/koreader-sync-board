package library

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"image/color"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// coverEnv — сканер на настоящих файлах со счётчиком открытий ради обложки.
type coverEnv struct {
	*testEnv
	covers *Covers
	root   string
	opens  int
	// onOpen подменяет открытие; nil — обычное os.OpenInRoot.
	onOpen func(root, name string) (*os.File, error)
}

func newCoverEnv(t *testing.T, covers *Covers) *coverEnv {
	t.Helper()
	if covers == nil {
		covers = NewCovers(filepath.Join(t.TempDir(), "covers"))
	}
	e := &coverEnv{testEnv: newEnv(t, WithCovers(covers)), covers: covers, root: t.TempDir()}
	e.sc.open = func(root, name string) (*os.File, error) {
		e.opens++
		if e.onOpen != nil {
			return e.onOpen(root, name)
		}
		return os.OpenInRoot(root, name)
	}
	return e
}

func coverEPUB(t *testing.T, c color.Color) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range map[string][]byte{
		"META-INF/container.xml": []byte(`<container><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`),
		"content.opf": []byte(`<package><metadata><meta name="cover" content="c"/></metadata>` +
			`<manifest><item id="c" href="c.jpg" media-type="image/jpeg"/></manifest></package>`),
		"c.jpg": encodeJPEG(t, 30, 40, c),
	} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	red  = color.RGBA{R: 255, A: 255}
	blue = color.RGBA{B: 255, A: 255}
)

func (e *coverEnv) put(name string, data []byte, mtime time.Time) {
	e.t.Helper()
	path := filepath.Join(e.root, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

// cover — значение колонки cover файла и признак проверки.
func (e *coverEnv) cover(name string) (string, bool) {
	e.t.Helper()
	var (
		id      sql.NullString
		checked bool
	)
	if err := e.db.Read.QueryRowContext(e.ctx,
		`SELECT cover, cover_checked FROM library_files WHERE path = ?`, name).Scan(&id, &checked); err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
	return id.String, checked
}

func (e *coverEnv) thumbs() []string {
	e.t.Helper()
	entries, err := os.ReadDir(e.covers.dir)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, d := range entries {
		out = append(out, d.Name())
	}
	return out
}

var t0 = time.Unix(1_700_000_000, 0).UTC()

type atomicTime struct{ v atomic.Int64 }

func (a *atomicTime) set(t time.Time) { a.v.Store(t.Unix()) }
func (a *atomicTime) get() time.Time  { return time.Unix(a.v.Load(), 0).UTC() }

// scannedAt — scanned_at корня.
func (e *coverEnv) scannedAt(id int64) *time.Time { return e.testEnv.root(id).ScannedAt }

func TestCoverNewBook(t *testing.T) {
	e := newCoverEnv(t, nil)
	e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, red), t0)
	e.scan()
	id, checked := e.cover("a.epub")
	if id == "" || !checked {
		t.Fatalf("cover %q, checked %v", id, checked)
	}
	if f, err := e.covers.Open(id); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
}

func TestCoverBackfillWithoutRehash(t *testing.T) {
	e := newCoverEnv(t, nil)
	e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, red), t0)
	e.scan()
	if _, err := e.db.Write.ExecContext(e.ctx, `UPDATE library_files SET cover = NULL, cover_checked = 0`); err != nil {
		t.Fatal(err)
	}
	e.reads.Store(0)
	e.scan()
	if id, checked := e.cover("a.epub"); id == "" || !checked {
		t.Fatalf("cover %q, checked %v", id, checked)
	}
	if n := e.reads.Load(); n != 0 {
		t.Errorf("rehashed %d files", n)
	}
}

func TestCoverSecondScanDoesNotOpen(t *testing.T) {
	e := newCoverEnv(t, nil)
	e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, red), t0)
	e.put("b.fb2", []byte(`<FictionBook><description><title-info/></description></FictionBook>`), t0)
	e.scan()
	if e.opens != 2 {
		t.Fatalf("first scan opened %d files", e.opens)
	}
	if id, checked := e.cover("b.fb2"); id != "" || !checked {
		t.Errorf("no cover: %q, checked %v", id, checked)
	}
	e.opens = 0
	e.reads.Store(0)
	e.scan()
	if e.opens != 0 || e.reads.Load() != 0 {
		t.Errorf("second scan: %d opens, %d reads", e.opens, e.reads.Load())
	}
}

func TestCoverReplacedFile(t *testing.T) {
	e := newCoverEnv(t, nil)
	e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, red), t0)
	e.scan()
	before, _ := e.cover("a.epub")
	e.put("a.epub", coverEPUB(t, blue), t0.Add(time.Hour))
	e.scan()
	after, checked := e.cover("a.epub")
	if after == "" || after == before || !checked {
		t.Fatalf("before %q, after %q, checked %v", before, after, checked)
	}
	if got := e.thumbs(); len(got) != 1 || got[0] != after+".jpg" {
		t.Errorf("thumbnails %v", got)
	}
}

func TestCoverReadErrorIsRetried(t *testing.T) {
	e := newCoverEnv(t, nil)
	e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, red), t0)
	e.scan()
	before, _ := e.cover("a.epub")

	e.put("a.epub", coverEPUB(t, blue), t0.Add(time.Hour))
	e.onOpen = func(string, string) (*os.File, error) {
		return nil, &fs.PathError{Op: "open", Path: "a.epub", Err: syscall.EIO}
	}
	e.scan()
	if id, checked := e.cover("a.epub"); id != before || checked {
		t.Fatalf("after failure: %q (was %q), checked %v", id, before, checked)
	}
	if got := e.thumbs(); len(got) != 1 {
		t.Errorf("old thumbnail must stay: %v", got)
	}

	e.onOpen = nil
	e.scan()
	if id, checked := e.cover("a.epub"); id == before || id == "" || !checked {
		t.Fatalf("after retry: %q, checked %v", id, checked)
	}
}

func TestCoverDamagedFileIsFinal(t *testing.T) {
	e := newCoverEnv(t, nil)
	e.addRoot(e.root)
	e.put("a.epub", []byte("not a zip"), t0)
	e.scan()
	if id, checked := e.cover("a.epub"); id != "" || !checked {
		t.Fatalf("cover %q, checked %v", id, checked)
	}
}

func TestCoverOtherFormatsNotOpened(t *testing.T) {
	e := newCoverEnv(t, nil)
	e.addRoot(e.root)
	e.put("a.pdf", []byte("%PDF-1.4"), t0)
	e.scan()
	if e.opens != 0 {
		t.Errorf("opened %d files", e.opens)
	}
	if n := e.count(`SELECT count(*) FROM library_files`); n != 1 {
		t.Errorf("files %d", n)
	}
}

// Подмена симлинком между обходом и проходом: настоящее os.OpenInRoot.
func TestCoverSymlinkOutsideRoot(t *testing.T) {
	e := newCoverEnv(t, nil)
	outside := filepath.Join(t.TempDir(), "secret.epub")
	if err := os.WriteFile(outside, coverEPUB(t, red), 0o644); err != nil {
		t.Fatal(err)
	}
	e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, blue), t0)
	path := filepath.Join(e.root, "a.epub")
	e.onRead = func(string) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
	}
	e.scan()
	if id, checked := e.cover("a.epub"); id != "" || checked {
		t.Fatalf("cover %q, checked %v", id, checked)
	}
}

func TestCoverCancelSkipsCleanup(t *testing.T) {
	e := newCoverEnv(t, nil)
	e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, red), t0)
	e.put("b.epub", coverEPUB(t, blue), t0)
	stale, err := e.covers.save([]byte("stale"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.onOpen = func(root, name string) (*os.File, error) {
		cancel()
		return os.OpenInRoot(root, name)
	}
	e.sc.scanAll(ctx)
	if e.opens != 1 {
		t.Errorf("opened %d files after cancel", e.opens)
	}
	if f, err := e.covers.Open(stale); err != nil {
		t.Errorf("cleanup ran: %v", err)
	} else {
		f.Close()
	}
}

func TestCoverCleanupAfterRootDeleted(t *testing.T) {
	e := newCoverEnv(t, nil)
	r := e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, red), t0)
	e.scan()
	if len(e.thumbs()) != 1 {
		t.Fatal("no thumbnail")
	}
	if err := e.st.DeleteRoot(e.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	e.scan()
	if got := e.thumbs(); len(got) != 0 {
		t.Errorf("thumbnails %v", got)
	}
}

func TestCoverSharedThumbnail(t *testing.T) {
	e := newCoverEnv(t, nil)
	e.addRoot(e.root)
	img := coverEPUB(t, red)
	e.put("a.epub", img, t0)
	e.put("b.epub", img, t0)
	e.scan()
	a, _ := e.cover("a.epub")
	b, _ := e.cover("b.epub")
	if a == "" || a != b {
		t.Fatalf("a %q, b %q", a, b)
	}
	if err := os.Remove(filepath.Join(e.root, "a.epub")); err != nil {
		t.Fatal(err)
	}
	e.scan()
	if got := e.thumbs(); len(got) != 1 {
		t.Errorf("thumbnails %v", got)
	}
}

func TestCoverWriteErrorStopsPass(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	write(t, file, "x")
	e := newCoverEnv(t, NewCovers(filepath.Join(file, "covers")))
	e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, red), t0)
	e.put("b.epub", coverEPUB(t, blue), t0)
	e.scan()
	if e.opens != 1 {
		t.Errorf("opened %d files", e.opens)
	}
	if n := e.count(`SELECT count(*) FROM library_files WHERE cover_checked = 1`); n != 0 {
		t.Errorf("checked %d files", n)
	}
}

func TestCoverPassKeepsScanTimes(t *testing.T) {
	e := newCoverEnv(t, nil)
	r := e.addRoot(e.root)
	e.put("a.epub", coverEPUB(t, red), t0)
	var clock atomicTime
	clock.set(t0)
	e.st.now = clock.get

	type snapshot struct {
		running bool
		scanned *time.Time
		err     error
	}
	during := make(chan snapshot, 1)
	e.onOpen = func(root, name string) (*os.File, error) {
		sum, err := e.sc.Summary(e.ctx)
		during <- snapshot{sum.Running, e.scannedAt(r.ID), err}
		clock.set(t0.Add(time.Hour))
		return os.OpenInRoot(root, name)
	}

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { e.sc.Run(ctx); close(done) }()
	e.sc.Trigger()
	got := <-during
	if got.err != nil || !got.running || got.scanned == nil || !got.scanned.Equal(t0) {
		t.Fatalf("during the pass: %+v", got)
	}
	for {
		sum, err := e.sc.Summary(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !sum.Running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if s := e.scannedAt(r.ID); s == nil || !s.Equal(t0) {
		t.Errorf("scanned_at after the pass: %v", s)
	}
}
