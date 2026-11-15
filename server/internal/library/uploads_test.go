package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

// uploads создаёт служебный корень загрузок и возвращает его каталог.
func (e *testEnv) uploads() string {
	e.t.Helper()
	dir := filepath.Join(e.t.TempDir(), "uploads")
	if err := e.st.EnsureManagedRoot(e.ctx, ManagedUploads, dir); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

func (e *testEnv) uploadsRootID() int64 {
	e.t.Helper()
	r, err := e.st.uploadsRoot(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return r.ID
}

func TestUploadsRootScannedButHidden(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	e.addRoot(src)
	dir := e.uploads()
	e.managed()
	write(t, filepath.Join(src, "Пикник.fb2"), "fb2")
	write(t, filepath.Join(dir, "Пикник.epub"), "epub")
	e.book("Пикник.fb2", "Пикник", "Стругацкий")
	e.book("Пикник.epub", "Пикник", "Стругацкий")
	e.scan()

	roots, err := e.st.Roots(e.ctx)
	if err != nil || len(roots) != 1 || roots[0].Path != src {
		t.Fatalf("roots %+v, %v", roots, err)
	}
	if sum, err := e.sc.Summary(e.ctx); err != nil || sum.Roots != 1 || sum.Files != 2 {
		t.Fatalf("summary %+v, %v", sum, err)
	}
	book := e.bookOf("Пикник.epub")
	if e.bookOf("Пикник.fb2") != book {
		t.Fatal("uploaded file not grouped with the root file")
	}
	if f := e.file(book, e.fileID("Пикник.epub")); !f.Uploaded || f.Converted {
		t.Fatalf("uploaded %+v", f)
	}
	if f := e.file(book, e.fileID("Пикник.fb2")); f.Uploaded || f.Converted {
		t.Fatalf("regular %+v", f)
	}
	if r := e.root(e.uploadsRootID()); r.Managed != ManagedUploads || r.ScannedAt == nil || r.Error != nil {
		t.Fatalf("uploads root %+v", r)
	}
	if err := e.st.DeleteRoot(e.ctx, e.uploadsRootID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete uploads root: %v", err)
	}
	if _, err := e.st.AddRoot(e.ctx, dir, ""); !errors.Is(err, ErrRootExists) {
		t.Fatalf("uploads dir as root: %v", err)
	}
}

func TestManagedRootsFollowDataDir(t *testing.T) {
	e := newEnv(t)
	for _, base := range []string{t.TempDir(), t.TempDir()} {
		for _, kind := range []ManagedKind{ManagedConverted, ManagedUploads} {
			if err := e.st.EnsureManagedRoot(e.ctx, kind, filepath.Join(base, kind.dirName())); err != nil {
				t.Fatal(err)
			}
		}
		for _, kind := range []ManagedKind{ManagedConverted, ManagedUploads} {
			want := filepath.Join(base, kind.dirName())
			if got := e.strings(`SELECT path FROM library_roots WHERE managed = ?`, kind); len(got) != 1 || got[0] != want {
				t.Fatalf("kind %d: %v, want %s", kind, got, want)
			}
			if _, err := os.Stat(want); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := e.count(`SELECT count(*) FROM library_roots`); n != 2 {
		t.Fatalf("%d roots", n)
	}
}

func (k ManagedKind) dirName() string {
	if k == ManagedUploads {
		return "uploads"
	}
	return "converted"
}

func TestEmptyFileSkipped(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	r := e.addRoot(dir)
	write(t, filepath.Join(dir, "a.epub"), "a")
	write(t, filepath.Join(dir, "b.epub"), "")
	e.scan()
	if got := e.paths(r.ID); len(got) != 1 || got[0] != "a.epub" {
		t.Fatalf("first scan %v", got)
	}
	write(t, filepath.Join(dir, "b.epub"), "b")
	e.scan()
	if got := e.paths(r.ID); len(got) != 2 {
		t.Fatalf("second scan %v", got)
	}
	// Файл, обнулённый после индексации, уходит из индекса.
	write(t, filepath.Join(dir, "a.epub"), "")
	e.scan()
	if got := e.paths(r.ID); len(got) != 1 || got[0] != "b.epub" {
		t.Fatalf("third scan %v", got)
	}
}

// Строку файла удалили, пока сканер его читал: пакет не падает, файл вносится заново.
func TestApplyRowDeletedDuringScan(t *testing.T) {
	e := newEnv(t)
	dir := e.uploads()
	write(t, filepath.Join(dir, "a.epub"), "first")
	write(t, filepath.Join(dir, "b.epub"), "b")
	e.scan()
	id := e.fileID("a.epub")
	write(t, filepath.Join(dir, "a.epub"), "second version")
	e.onRead = func(path string) {
		if filepath.Base(path) == "a.epub" {
			if err := e.st.removeFiles(e.ctx, []int64{id}); err != nil {
				t.Error(err)
			}
		}
	}
	e.scan()
	e.onRead = nil
	if r := e.root(e.uploadsRootID()); r.Error != nil {
		t.Fatalf("root error %q", *r.Error)
	}
	if got := e.paths(e.uploadsRootID()); len(got) != 2 {
		t.Fatalf("paths %v", got)
	}
	if e.fileID("a.epub") == id {
		t.Fatal("deleted row reused")
	}
}

// walkLog записывает обходы корней и следит, чтобы они не шли одновременно.
type walkLog struct {
	mu         sync.Mutex
	walks      []string
	active     int
	concurrent bool
	// gate — если не nil, первый обход ждёт его закрытия.
	gate    chan struct{}
	entered chan string
}

func newWalkLog() *walkLog { return &walkLog{entered: make(chan string, 64)} }

func (l *walkLog) walk(root string, fn fs.WalkDirFunc) error {
	l.mu.Lock()
	l.walks = append(l.walks, root)
	first := len(l.walks) == 1
	l.active++
	if l.active > 1 {
		l.concurrent = true
	}
	gate := l.gate
	l.mu.Unlock()
	l.entered <- root
	if first && gate != nil {
		<-gate
	}
	defer func() { l.mu.Lock(); l.active--; l.mu.Unlock() }()
	return filepath.WalkDir(root, fn)
}

func (l *walkLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.walks)
}

// run запускает Run и возвращает остановку.
func (e *testEnv) run() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.sc.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

// idle ждёт, пока сканер не закончит всё из очереди.
func (e *testEnv) idle() {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := e.sc.Summary(e.ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		if !s.Running {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatal("scan did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Сканирование только по запросу: загрузка видна без ручного скана, обычные корни не обходятся.
func TestTriggerUploadsIntervalZero(t *testing.T) {
	l := newWalkLog()
	e := newEnv(t, WithWalk(l.walk))
	e.addRoot(t.TempDir())
	dir := e.uploads()
	write(t, filepath.Join(dir, "a.epub"), "a")
	defer e.run()()

	e.sc.TriggerUploads()
	if s, err := e.sc.Summary(e.ctx); err != nil || !s.Running {
		t.Fatalf("summary after TriggerUploads %+v %v", s, err)
	}
	e.idle()
	if got := l.list(); !slices.Equal(got, []string{realPath(t, dir)}) {
		t.Fatalf("walks %v", got)
	}
	if got := e.paths(e.uploadsRootID()); !slices.Equal(got, []string{"a.epub"}) {
		t.Fatalf("paths %v", got)
	}
}

// Пачка файлов: запросы во время скана загрузок сливаются в один повторный, полный и
// загрузочный не идут одновременно.
func TestTriggerUploadsMerged(t *testing.T) {
	l := newWalkLog()
	l.gate = make(chan struct{})
	e := newEnv(t, WithWalk(l.walk))
	regular := e.addRoot(t.TempDir())
	dir := e.uploads()
	defer e.run()()

	e.sc.TriggerUploads()
	<-l.entered
	for i := range 10 {
		write(t, filepath.Join(dir, fmt.Sprintf("%d.epub", i)), fmt.Sprint(i))
		e.sc.TriggerUploads()
	}
	close(l.gate)
	<-l.entered
	e.idle()
	uploads := realPath(t, dir)
	if got := l.list(); !slices.Equal(got, []string{uploads, uploads}) {
		t.Fatalf("walks %v", got)
	}
	if n := len(e.paths(e.uploadsRootID())); n != 10 {
		t.Fatalf("%d files indexed", n)
	}

	// Полный скан идёт, загрузка ждёт его конца.
	l.mu.Lock()
	l.walks, l.gate = nil, make(chan struct{})
	l.mu.Unlock()
	e.sc.Trigger()
	<-l.entered
	e.sc.TriggerUploads()
	time.Sleep(20 * time.Millisecond)
	close(l.gate)
	e.idle()
	got := l.list()
	if len(got) != 3 || got[0] != realPath(t, regular.Path) || got[2] != uploads {
		t.Fatalf("walks %v", got)
	}
	if l.concurrent {
		t.Fatal("scans overlapped")
	}
}

// Полный скан в очереди поглощает скан загрузок при любом порядке готовности каналов.
func TestFullScanAbsorbsUploads(t *testing.T) {
	for i := range 20 {
		l := newWalkLog()
		e := newEnv(t, WithWalk(l.walk))
		regular := e.addRoot(t.TempDir())
		dir := e.uploads()
		if i%2 == 0 {
			e.sc.Trigger()
			e.sc.TriggerUploads()
		} else {
			e.sc.TriggerUploads()
			e.sc.Trigger()
		}
		stop := e.run()
		e.idle()
		stop()
		want := []string{realPath(t, regular.Path), realPath(t, dir)}
		if got := l.list(); !slices.Equal(got, want) {
			t.Fatalf("iteration %d: walks %v, want %v", i, got, want)
		}
	}
}

// Удалён последний файл: скан загрузок убирает его без ошибки «корень пуст».
func TestUploadsRootEmptied(t *testing.T) {
	e := newEnv(t)
	dir := e.uploads()
	write(t, filepath.Join(dir, "a.epub"), "a")
	e.sc.scanUploads(e.ctx)
	if n := len(e.paths(e.uploadsRootID())); n != 1 {
		t.Fatalf("%d files", n)
	}
	if err := os.Remove(filepath.Join(dir, "a.epub")); err != nil {
		t.Fatal(err)
	}
	e.sc.scanUploads(e.ctx)
	if n := len(e.paths(e.uploadsRootID())); n != 0 {
		t.Fatalf("%d files after delete", n)
	}
	if r := e.root(e.uploadsRootID()); r.Error != nil {
		t.Fatalf("root error %q", *r.Error)
	}
	if n := e.count(`SELECT count(*) FROM library_books`); n != 0 {
		t.Fatalf("%d books", n)
	}
}

// NAS отключён: скан загрузок проверяет обложки только своего корня.
func TestUploadsScanCoversOwnRoot(t *testing.T) {
	e := newCoverEnv(t, nil)
	regular := e.addRoot(e.root)
	e.put("nas.epub", coverEPUB(t, red), time.Unix(1000, 0))
	e.sc.scanRoot(e.ctx, e.testEnv.root(regular.ID))
	dir := e.uploads()
	if err := os.WriteFile(filepath.Join(dir, "up.epub"), coverEPUB(t, blue), 0o644); err != nil {
		t.Fatal(err)
	}
	var opened []string
	e.onOpen = func(root, name string) (*os.File, error) {
		opened = append(opened, name)
		return os.OpenInRoot(root, name)
	}
	e.sc.scanUploads(e.ctx)
	if !slices.Equal(opened, []string{"up.epub"}) {
		t.Fatalf("opened %v", opened)
	}
	if n := e.count(`SELECT count(*) FROM library_files WHERE cover_checked = 0`); n != 1 {
		t.Fatalf("%d unchecked", n)
	}
	if got := e.strings(`SELECT cover FROM library_files WHERE path = 'up.epub'`); got[0] == "" {
		t.Fatal("uploaded cover not extracted")
	}
}

func TestBookByHash(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.addRoot(dir)
	write(t, filepath.Join(dir, "a.epub"), "content")
	e.scan()
	sum, err := hashing.PartialMD5(filepath.Join(dir, "a.epub"))
	if err != nil {
		t.Fatal(err)
	}
	if id, err := e.st.BookByHash(e.ctx, sum); err != nil || id != e.bookOf("a.epub") {
		t.Fatalf("by content: %d, %v", id, err)
	}
	// md5 имени тоже лежит в хешах файла, но дубликатом не считается.
	for _, h := range hashing.FilenameMD5Variants("a.epub") {
		if id, err := e.st.BookByHash(e.ctx, h); err != nil || id != 0 {
			t.Fatalf("by name %s: %d, %v", h, id, err)
		}
	}
}

func TestDeleteUploaded(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	e.addRoot(src)
	dir := e.uploads()
	write(t, filepath.Join(src, "a.fb2"), "fb2")
	write(t, filepath.Join(dir, "sub/a.epub"), "epub")
	write(t, filepath.Join(dir, "b.epub"), "b")
	e.book("a.fb2", "A", "X")
	e.book("a.epub", "A", "X")
	e.scan()
	book := e.bookOf("sub/a.epub")

	for path, want := range map[string]ManagedKind{"a.fb2": ManagedNone, "sub/a.epub": ManagedUploads} {
		if kind, err := e.st.FileKind(e.ctx, e.fileID(path)); err != nil || kind != want {
			t.Fatalf("%s: kind %d, %v", path, kind, err)
		}
	}
	if _, err := e.st.FileKind(e.ctx, 9999); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing kind: %v", err)
	}
	if _, err := e.st.DeleteUploaded(e.ctx, e.fileID("a.fb2")); !errors.Is(err, ErrReadOnlyFile) {
		t.Fatalf("regular: %v", err)
	}
	path, err := e.st.DeleteUploaded(e.ctx, e.fileID("sub/a.epub"))
	if err != nil || path != "sub/a.epub" {
		t.Fatalf("delete: %q, %v", path, err)
	}
	if e.bookOf("a.fb2") != book {
		t.Fatal("book of the remaining file changed")
	}
	// Единственный файл книги уносит книгу.
	only := e.bookOf("b.epub")
	if _, err := e.st.DeleteUploaded(e.ctx, e.fileID("b.epub")); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM library_books WHERE id = ?`, only); n != 0 {
		t.Fatal("empty book left")
	}
	if _, err := e.st.DeleteUploaded(e.ctx, 9999); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

// Файл удалили, пока скан загрузок его читал: строка-призрак, которую вернул скан,
// убирается следующим сканом загрузок, который ставит удаление, без ручного скана.
func TestDeleteDuringUploadsScan(t *testing.T) {
	var (
		e        *testEnv
		dir      string
		id       int64
		armed    atomic.Bool
		ghostHit atomic.Bool
	)
	// Удаление — после чтения файла: скан уже посчитал хеш и придёт в apply со строкой,
	// которой нет.
	hash := func(path string) (string, error) {
		sum, err := hashing.PartialMD5(path)
		if filepath.Base(path) == "a.epub" && armed.CompareAndSwap(true, false) {
			// То же, что делает обработчик удаления: индекс, диск, скан загрузок.
			rel, derr := e.st.DeleteUploaded(e.ctx, id)
			if derr != nil {
				t.Error(derr)
			}
			if derr := os.Remove(filepath.Join(dir, rel)); derr != nil {
				t.Error(derr)
			}
			e.sc.TriggerUploads()
			ghostHit.Store(true)
		}
		return sum, err
	}
	parse := func(string, string) (meta.Book, error) { return meta.Book{}, nil }
	e = newEnv(t, WithReader(hash, parse))
	dir = e.uploads()
	write(t, filepath.Join(dir, "a.epub"), "first")
	write(t, filepath.Join(dir, "b.epub"), "b")
	e.sc.scanUploads(e.ctx)
	id = e.fileID("a.epub")
	write(t, filepath.Join(dir, "a.epub"), "second version")
	armed.Store(true)

	defer e.run()()
	e.sc.TriggerUploads()
	e.idle()
	if !ghostHit.Load() {
		t.Fatal("deletion hook did not run")
	}
	if got := e.paths(e.uploadsRootID()); !slices.Equal(got, []string{"b.epub"}) {
		t.Fatalf("paths %v", got)
	}
	if r := e.root(e.uploadsRootID()); r.Error != nil {
		t.Fatalf("root error %q", *r.Error)
	}
}
