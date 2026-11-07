package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

type testEnv struct {
	t     *testing.T
	ctx   context.Context
	db    *store.DB
	st    *Store
	sc    *Scanner
	reads atomic.Int32
	// books — метаданные по базовому имени файла для подменного разбора.
	books map[string]meta.Book
	// onRead вызывается при каждом чтении файла сканером.
	onRead func(path string)
}

func newEnv(t *testing.T, opts ...Option) *testEnv {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &testEnv{t: t, ctx: ctx, db: db, st: NewStore(db), books: map[string]meta.Book{}}
	hash := func(path string) (string, error) {
		e.reads.Add(1)
		if e.onRead != nil {
			e.onRead(path)
		}
		return hashing.PartialMD5(path)
	}
	parse := func(path, format string) (meta.Book, error) {
		if b, ok := e.books[filepath.Base(path)]; ok {
			return b, nil
		}
		return meta.Parse(path, format)
	}
	e.sc = NewScanner(e.st, 0, slog.New(slog.DiscardHandler), append([]Option{WithReader(hash, parse)}, opts...)...)
	return e
}

func (e *testEnv) addRoot(path string) Root {
	e.t.Helper()
	r, err := e.st.AddRoot(e.ctx, path, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *testEnv) scan() { e.sc.scanAll(e.ctx) }

func (e *testEnv) root(id int64) Root {
	e.t.Helper()
	r, err := e.st.Root(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// paths — проиндексированные пути корня по порядку.
func (e *testEnv) paths(rootID int64) []string {
	e.t.Helper()
	return e.strings(`SELECT path FROM library_files WHERE root_id = ? ORDER BY path`, rootID)
}

func (e *testEnv) strings(q string, args ...any) []string {
	e.t.Helper()
	rows, err := e.db.Read.QueryContext(e.ctx, q, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s.String)
	}
	return out
}

func (e *testEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read.QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// bookOf — id книги файла.
func (e *testEnv) bookOf(path string) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT book_id FROM library_files WHERE path = ?`, path).Scan(&id); err != nil {
		e.t.Fatalf("%s: %v", path, err)
	}
	return id
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSelectFiles(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(outside, "out.epub"), "x")
	for _, p := range []string{
		"a.EPUB", "._a.epub", "notes.txt", "a.sdr/metadata.epub.lua", "a.sdr/inner.epub",
		".Trash/b.fb2", "x.azw", "y.azw3", "z.fb2.zip", "sub/c.Pdf", "sub/d.djvu", "m.mobi", "k.cbz",
	} {
		write(t, filepath.Join(dir, p), p)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "a.EPUB"), filepath.Join(dir, "link.epub")); err != nil {
		t.Fatal(err)
	}
	// Корень — симлинк на каталог: обходится каталог, пути — относительно корня.
	link := filepath.Join(t.TempDir(), "books")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	r := e.addRoot(link)
	e.scan()
	want := []string{"a.EPUB", "k.cbz", "m.mobi", "sub/c.Pdf", "sub/d.djvu", "x.azw", "z.fb2.zip"}
	if got := e.paths(r.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := e.strings(`SELECT format FROM library_files WHERE path = 'z.fb2.zip'`); got[0] != "fb2.zip" {
		t.Fatalf("format %q", got)
	}
	if r := e.root(r.ID); r.Error != nil || r.Files != len(want) || r.ScannedAt == nil {
		t.Fatalf("root %+v", r)
	}
}

func TestScanHashes(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	nfd := norm.NFD.String("Пикник.epub")
	path := filepath.Join(dir, nfd)
	write(t, path, strings.Repeat("книга", 3000))
	r := e.addRoot(dir)
	e.scan()
	partial, err := hashing.PartialMD5(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.strings(`SELECT partial_md5 FROM library_files`); got[0] != partial {
		t.Fatalf("partial_md5 %q, want %q", got, partial)
	}
	nfc := hashing.FilenameMD5Variants(norm.NFC.String("Пикник.epub"))[0]
	for _, h := range []string{partial, nfc, hashing.FilenameMD5Variants(nfd)[0]} {
		if got := e.strings(`SELECT f.path FROM library_file_hashes h JOIN library_files f ON f.id = h.file_id WHERE h.hash = ?`, h); !reflect.DeepEqual(got, []string{nfd}) {
			t.Errorf("hash %s: %q", h, got)
		}
	}
	if r := e.root(r.ID); r.Files != 1 {
		t.Fatalf("files %d", r.Files)
	}
}

func TestRescanCache(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.pdf"), "a")
	write(t, filepath.Join(dir, "b.pdf"), "b")
	e.addRoot(dir)
	e.scan()
	if n := e.reads.Load(); n != 2 {
		t.Fatalf("first scan read %d files", n)
	}
	before := e.strings(`SELECT indexed_at || partial_md5 FROM library_files ORDER BY id`)
	e.scan()
	if n := e.reads.Load(); n != 2 {
		t.Fatalf("unchanged rescan read %d files", n-2)
	}
	if after := e.strings(`SELECT indexed_at || partial_md5 FROM library_files ORDER BY id`); !reflect.DeepEqual(before, after) {
		t.Fatal("unchanged rescan changed the index")
	}

	// Подмена файла с тем же именем: mtime другой — хеш пересчитан.
	write(t, filepath.Join(dir, "a.pdf"), "replaced")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a.pdf"), future, future); err != nil {
		t.Fatal(err)
	}
	e.scan()
	if n := e.reads.Load(); n != 3 {
		t.Fatalf("changed file: %d reads, want 3", n)
	}
	want, _ := hashing.PartialMD5(filepath.Join(dir, "a.pdf"))
	if got := e.strings(`SELECT partial_md5 FROM library_files WHERE path = 'a.pdf'`); got[0] != want {
		t.Fatal("partial MD5 not recomputed")
	}
}

func TestDeletedFile(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.books["a.epub"] = meta.Book{Title: "A"}
	e.books["b.epub"] = meta.Book{Title: "B"}
	write(t, filepath.Join(dir, "a.epub"), "a")
	write(t, filepath.Join(dir, "b.epub"), "b")
	r := e.addRoot(dir)
	e.scan()
	if err := os.Remove(filepath.Join(dir, "a.epub")); err != nil {
		t.Fatal(err)
	}
	e.scan()
	if got := e.paths(r.ID); !reflect.DeepEqual(got, []string{"b.epub"}) {
		t.Fatalf("paths %q", got)
	}
	if got := e.strings(`SELECT title FROM library_books`); !reflect.DeepEqual(got, []string{"B"}) {
		t.Fatalf("books %q", got)
	}
	if n := e.count(`SELECT (SELECT count(*) FROM library_book_keys) + (SELECT count(*) FROM library_file_hashes h WHERE NOT EXISTS (SELECT 1 FROM library_files f WHERE f.id = h.file_id))`); n != 1 {
		t.Fatalf("keys + orphan hashes = %d, want 1 key", n)
	}
}

func TestRootUnavailable(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(t.TempDir(), "books")
	write(t, filepath.Join(dir, "a.epub"), "a")
	write(t, filepath.Join(dir, "sub/b.epub"), "b")
	r := e.addRoot(dir)
	e.scan()

	clock := time.Unix(2_000_000_000, 0)
	e.st.now = func() time.Time { return clock }
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	e.scan()
	got := e.root(r.ID)
	if got.Error == nil || !strings.Contains(*got.Error, "root unavailable") || got.Files != 2 || !got.ScannedAt.Equal(clock) {
		t.Fatalf("root %+v", got)
	}

	// Каталог на месте, но пуст: так выглядит неподключённый NAS.
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.scan()
	if got := e.root(r.ID); got.Error == nil || !strings.Contains(*got.Error, "root is empty") || got.Files != 2 {
		t.Fatalf("empty root %+v", got)
	}

	write(t, filepath.Join(dir, "a.epub"), "a")
	e.scan()
	if got := e.root(r.ID); got.Error != nil || got.Files != 1 {
		t.Fatalf("recovered root %+v", got)
	}
}

func TestNewEmptyRoot(t *testing.T) {
	e := newEnv(t)
	r := e.addRoot(t.TempDir())
	e.scan()
	if got := e.root(r.ID); got.Error != nil || got.ScannedAt == nil {
		t.Fatalf("root %+v", got)
	}
}

func TestUnreadableSubdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	e := newEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "private/p.epub"), "p")
	write(t, filepath.Join(dir, "old.epub"), "o")
	r := e.addRoot(dir)
	e.scan()

	private := filepath.Join(dir, "private")
	if err := os.Chmod(private, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(private, 0o755) })
	if err := os.Remove(filepath.Join(dir, "old.epub")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "new.epub"), "n")
	e.scan()
	if got := e.paths(r.ID); !reflect.DeepEqual(got, []string{"new.epub", "private/p.epub"}) {
		t.Fatalf("paths %q", got)
	}
	if got := e.root(r.ID); got.Error == nil || !strings.Contains(*got.Error, private) {
		t.Fatalf("root %+v", got)
	}
}

func TestCancelKeepsUnvisited(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cancelOnFile atomic.Bool
	walk := func(root string, fn fs.WalkDirFunc) error {
		return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if cancelOnFile.Load() && d != nil && !d.IsDir() {
				cancel()
			}
			return fn(p, d, err)
		})
	}
	e := newEnv(t, WithWalk(walk))
	dir := t.TempDir()
	for _, p := range []string{"a/1.pdf", "b/2.pdf", "c/3.pdf"} {
		write(t, filepath.Join(dir, p), p)
	}
	r := e.addRoot(dir)
	e.scan()
	scanned := e.root(r.ID).ScannedAt

	cancelOnFile.Store(true)
	e.sc.scanAll(ctx)
	if got := e.paths(r.ID); len(got) != 3 {
		t.Fatalf("paths after cancel %q", got)
	}
	if got := e.root(r.ID); !reflect.DeepEqual(got.ScannedAt, scanned) {
		t.Fatal("cancelled scan updated scanned_at")
	}
}

func TestCancelMidScanCommitsWholeBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e := newEnv(t)
	dir := t.TempDir()
	for i := range batchSize + 10 {
		name := fmt.Sprintf("%03d.epub", i)
		write(t, filepath.Join(dir, name), name)
		e.books[name] = meta.Book{Title: "T" + name}
	}
	e.onRead = func(string) {
		if e.reads.Load() == batchSize+5 {
			cancel()
		}
	}
	r := e.addRoot(dir)
	e.sc.scanAll(ctx)
	if n := len(e.paths(r.ID)); n != batchSize {
		t.Fatalf("indexed %d files, want one batch of %d", n, batchSize)
	}
	if n := e.count(`SELECT count(*) FROM library_books b WHERE title IS NULL OR NOT EXISTS (SELECT 1 FROM library_files f WHERE f.book_id = b.id)`); n != 0 {
		t.Fatalf("%d books without title or files", n)
	}
	if n := e.count(`SELECT count(*) FROM library_books`); n != batchSize {
		t.Fatalf("books %d", n)
	}
}

func TestRootDeletedDuringScan(t *testing.T) {
	e := newEnv(t)
	first, second := t.TempDir(), t.TempDir()
	write(t, filepath.Join(first, "a.pdf"), "a")
	write(t, filepath.Join(second, "b.pdf"), "b")
	r1 := e.addRoot(first)
	r2 := e.addRoot(second)
	e.onRead = func(path string) {
		if strings.HasPrefix(path, first) {
			if err := e.st.DeleteRoot(e.ctx, r1.ID); err != nil {
				t.Error(err)
			}
		}
	}
	e.scan()
	if got := e.paths(r2.ID); !reflect.DeepEqual(got, []string{"b.pdf"}) {
		t.Fatalf("second root %q", got)
	}
	if n := e.count(`SELECT count(*) FROM library_books`); n != 1 {
		t.Fatalf("books %d", n)
	}
}

func TestGrouping(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	strug := []string{"Аркадий Стругацкий", "Борис Стругацкий"}
	e.books["1-piknik.fb2"] = meta.Book{Title: "Пикник на обочине", Authors: strug, Language: "ru", Series: "Мир Полудня"}
	e.books["2-Piknik.epub"] = meta.Book{Title: "Пикник на обочине.", Authors: []string{"Стругацкий Аркадий", "Стругацкий Борис"}, Language: "en"}
	e.books["3-other.epub"] = meta.Book{Title: "Пикник на обочине", Authors: []string{"Другой Автор"}}
	e.books["0-notitle.epub"] = meta.Book{Authors: strug}
	for _, n := range []string{"0-notitle.epub", "1-piknik.fb2", "2-Piknik.epub", "3-other.epub", "4-a.pdf", "5-b.pdf"} {
		write(t, filepath.Join(dir, n), n)
	}
	e.addRoot(dir)
	e.scan()

	if e.bookOf("1-piknik.fb2") != e.bookOf("2-Piknik.epub") {
		t.Fatal("FB2 and EPUB of one book are separate")
	}
	if e.bookOf("1-piknik.fb2") == e.bookOf("3-other.epub") {
		t.Fatal("different authors share a book")
	}
	if e.bookOf("4-a.pdf") == e.bookOf("5-b.pdf") || e.bookOf("0-notitle.epub") == e.bookOf("1-piknik.fb2") {
		t.Fatal("files without title share a book")
	}
	if n := e.count(`SELECT count(*) FROM library_books WHERE title IS NULL`); n != 3 {
		t.Fatalf("books without title %d", n)
	}
	var title, authors, lang, series string
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT title, authors, language, series FROM library_books WHERE id = ?`,
		e.bookOf("2-Piknik.epub")).Scan(&title, &authors, &lang, &series); err != nil {
		t.Fatal(err)
	}
	if title != "Пикник на обочине" || authors != "Аркадий Стругацкий, Борис Стругацкий" || lang != "ru" || series != "Мир Полудня" {
		t.Fatalf("book metadata %q %q %q %q", title, authors, lang, series)
	}

	// Имитация ручного слияния: второй ключ указывает на книгу «Пикника».
	piknik := e.bookOf("1-piknik.fb2")
	key, _ := meta.Key("Roadside Picnic", []string{"Arkady Strugatsky"})
	if _, err := e.db.Write.ExecContext(e.ctx, `INSERT INTO library_book_keys (key, book_id) VALUES (?, ?)`, key, piknik); err != nil {
		t.Fatal(err)
	}
	e.books["6-en.epub"] = meta.Book{Title: "Roadside Picnic", Authors: []string{"Arkady Strugatsky"}}
	write(t, filepath.Join(dir, "6-en.epub"), "en")
	e.scan()
	if e.bookOf("6-en.epub") != piknik {
		t.Fatal("merge key did not attract the new file")
	}

	// Изменённый файл с новым ключом уходит в другую книгу, опустевшая удаляется.
	other := e.bookOf("3-other.epub")
	e.books["3-other.epub"] = meta.Book{Title: "Новое название"}
	write(t, filepath.Join(dir, "3-other.epub"), "changed")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "3-other.epub"), future, future); err != nil {
		t.Fatal(err)
	}
	e.scan()
	if e.bookOf("3-other.epub") == other {
		t.Fatal("file with a new key stayed in the old book")
	}
	if n := e.count(`SELECT count(*) FROM library_books WHERE id = ?`, other); n != 0 {
		t.Fatal("empty book not deleted")
	}
	if n := e.count(`SELECT count(*) FROM library_book_keys WHERE book_id = ?`, other); n != 0 {
		t.Fatal("keys of deleted book remain")
	}
}

func TestDeleteRoot(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.books["p.epub"] = meta.Book{Title: "Пикник"}
	write(t, filepath.Join(dir, "p.epub"), "p")
	r := e.addRoot(dir)
	e.scan()
	if err := e.st.DeleteRoot(e.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT (SELECT count(*) FROM library_files) + (SELECT count(*) FROM library_books) + (SELECT count(*) FROM library_file_hashes)`); n != 0 {
		t.Fatalf("%d rows left", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "p.epub")); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteRoot(e.ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAddRootValidation(t *testing.T) {
	e := newEnv(t)
	base := t.TempDir()
	books := filepath.Join(base, "books")
	write(t, filepath.Join(books, "fantasy/a.epub"), "a")
	write(t, filepath.Join(base, "file.epub"), "f")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Join(books, "fantasy"), alias); err != nil {
		t.Fatal(err)
	}
	r := e.addRoot(books)
	if r.Name != "books" || r.Path != books {
		t.Fatalf("root %+v", r)
	}
	cases := map[string]error{
		"books":                          ErrInvalidPath,
		filepath.Join(base, "file.epub"): ErrInvalidPath,
		filepath.Join(base, "missing"):   ErrInvalidPath,
		books + "/":                      ErrRootExists,
		books + "/fantasy/..":            ErrRootExists,
		filepath.Join(books, "fantasy"):  ErrRootOverlap,
		base:                             ErrRootOverlap,
		alias:                            ErrRootOverlap,
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(t.TempDir(), "locked")
		if err := os.Mkdir(locked, 0); err != nil {
			t.Fatal(err)
		}
		cases[locked] = ErrInvalidPath
	}
	for path, want := range cases {
		if _, err := e.st.AddRoot(e.ctx, path, ""); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", path, err, want)
		}
	}
	sib := t.TempDir()
	if r, err := e.st.AddRoot(e.ctx, sib, " Мои книги "); err != nil || r.Name != "Мои книги" {
		t.Fatalf("named root %+v %v", r, err)
	}
}

func TestTrigger(t *testing.T) {
	var (
		calls   atomic.Int32
		entered = make(chan struct{}, 4)
		release = make(chan struct{})
		once    sync.Once
	)
	walk := func(root string, fn fs.WalkDirFunc) error {
		calls.Add(1)
		entered <- struct{}{}
		if calls.Load() == 1 {
			<-release
		}
		return filepath.WalkDir(root, fn)
	}
	e := newEnv(t, WithWalk(walk))
	e.addRoot(t.TempDir())

	// Сводка до того, как Run забрал триггер, уже показывает скан.
	e.sc.Trigger()
	if s, err := e.sc.Summary(e.ctx); err != nil || !s.Running || s.StartedAt != nil {
		t.Fatalf("summary after Trigger %+v %v", s, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.sc.Run(ctx); close(done) }()
	defer func() {
		once.Do(func() { close(release) })
		cancel()
		<-done
	}()
	<-entered
	if s, _ := e.sc.Summary(e.ctx); !s.Running || s.StartedAt == nil {
		t.Fatalf("summary during scan %+v", s)
	}
	e.sc.Trigger()
	e.sc.Trigger()
	once.Do(func() { close(release) })
	<-entered
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := e.sc.Summary(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !s.Running {
			if s.FinishedAt == nil {
				t.Fatal("finished_at not set")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("walks %d, want 2", n)
	}
}

func TestRunIntervalZeroAndCancel(t *testing.T) {
	var calls atomic.Int32
	walk := func(root string, fn fs.WalkDirFunc) error {
		calls.Add(1)
		return filepath.WalkDir(root, fn)
	}
	e := newEnv(t, WithWalk(walk))
	e.addRoot(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.sc.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Fatalf("interval 0 scanned %d times by itself", n)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit on cancel")
	}

	// С периодом скан идёт сразу после старта.
	e.sc.interval = time.Hour
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { e.sc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no scan on start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSummaryEmpty(t *testing.T) {
	e := newEnv(t)
	s, err := e.sc.Summary(e.ctx)
	if err != nil || !reflect.DeepEqual(s, Summary{}) {
		t.Fatalf("%+v %v", s, err)
	}
}
