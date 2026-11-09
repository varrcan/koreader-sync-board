package convert

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

// Тестовый бинарник сам служит дочерним процессом, как ksb в serve.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "convert-fb2" {
		os.Exit(Child(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

const fb2Tail = `<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0"><description><title-info>
<genre>sf</genre><author><first-name>Аркадий</first-name><last-name>Стругацкий</last-name></author>
<book-title>Пикник</book-title><lang>ru</lang></title-info>
<document-info><id>6f1c2c1e-7d0e-4a57-9b57-0a4f6b0e1a11</id></document-info></description>
%s<body><section><title><p>Глава</p></title><p>Зона посещения</p></section></body></FictionBook>`

func fb2Doc(encoding, head string) string {
	return `<?xml version="1.0" encoding="` + encoding + `"?>` + fmt.Sprintf(fb2Tail, head)
}

type testEnv struct {
	t       *testing.T
	ctx     context.Context
	db      *store.DB
	st      *library.Store
	sc      *library.Scanner
	root    string
	dataDir string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &testEnv{t: t, ctx: ctx, db: db, st: library.NewStore(db), root: t.TempDir(), dataDir: t.TempDir()}
	e.sc = library.NewScanner(e.st, 0, slog.New(slog.DiscardHandler))
	scanCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { e.sc.Run(scanCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	if _, err := e.st.AddRoot(ctx, e.root, ""); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *testEnv) service(opts ...Option) *Service {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.ctx)
	s, err := New(ctx, e.st, e.dataDir, slog.New(slog.DiscardHandler), append([]Option{WithExecutable(self(e.t))}, opts...)...)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { cancel(); s.Wait(10 * time.Second) })
	return s
}

func self(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// script создаёт программу дочернего процесса из текста shell.
func (e *testEnv) script(body string) string {
	e.t.Helper()
	path := filepath.Join(e.t.TempDir(), "child.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		e.t.Fatal(err)
	}
	return path
}

func (e *testEnv) scan() {
	e.t.Helper()
	e.sc.Trigger()
	deadline := time.Now().Add(5 * time.Second)
	for {
		sum, err := e.sc.Summary(e.ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		if !sum.Running {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatal("scan did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// add кладёт файл в корень, сканирует его и возвращает id.
func (e *testEnv) add(name string, content []byte) int64 {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.root, name), content, 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.scan()
	var id int64
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT id FROM library_files WHERE path = ?`, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *testEnv) count(q string) int {
	e.t.Helper()
	var n int
	if err := e.db.Read.QueryRowContext(e.ctx, q).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *testEnv) noResult() {
	e.t.Helper()
	if n := e.count(`SELECT count(*) FROM library_files WHERE source_md5 IS NOT NULL`); n != 0 {
		e.t.Errorf("%d results in the index", n)
	}
	entries, err := os.ReadDir(filepath.Join(e.dataDir, "converted"))
	if err != nil || len(entries) != 0 {
		e.t.Errorf("converted dir: %v, %v", entries, err)
	}
	e.tmpClean()
}

func (e *testEnv) tmpClean() {
	e.t.Helper()
	if entries, err := os.ReadDir(filepath.Join(e.dataDir, "tmp")); err != nil || len(entries) != 0 {
		e.t.Errorf("tmp dir: %v, %v", entries, err)
	}
}

// result — путь результата id на диске.
func (e *testEnv) result(id int64) string {
	e.t.Helper()
	loc, err := e.st.FileLocation(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return filepath.Join(loc.Root, filepath.FromSlash(loc.Path))
}

// epubText — текст всех записей EPUB.
func epubText(t *testing.T, path string) string {
	t.Helper()
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var b strings.Builder
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}

func zipped(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestConvertEncodings(t *testing.T) {
	cp1251, err := charmap.Windows1251.NewEncoder().Bytes([]byte(fb2Doc("windows-1251", "")))
	if err != nil {
		t.Fatal(err)
	}
	utf16, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes([]byte(fb2Doc("utf-16", "")))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, file string
		content    []byte
		want       string
	}{
		{"utf-8", "Пикник.fb2", []byte(fb2Doc("utf-8", "")), "Пикник.epub"},
		{"utf-8 bom", "bom.fb2", append([]byte{0xEF, 0xBB, 0xBF}, fb2Doc("utf-8", "")...), "bom.epub"},
		{"windows-1251", "cp.fb2", cp1251, "cp.epub"},
		{"utf-16 bom", "wide.fb2", utf16, "wide.epub"},
		{"zip", "Архив.fb2.zip", zipped(t, "inner.fb2", fb2Doc("utf-8", "")), "Архив.epub"},
		{"upper case extension", "UP.FB2", []byte(fb2Doc("utf-8", "")), "UP.epub"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			src := e.add(tt.file, tt.content)
			id, err := e.service().Convert(e.ctx, src)
			if err != nil {
				t.Fatal(err)
			}
			path := e.result(id)
			if filepath.Base(path) != tt.want {
				t.Fatalf("name %q, want %q", filepath.Base(path), tt.want)
			}
			if text := epubText(t, path); !strings.Contains(text, "Зона посещения") {
				t.Fatal("text is missing or garbled")
			}
			got, err := meta.Parse(path, "epub")
			if err != nil || got.Title != "Пикник" {
				t.Fatalf("metadata %+v, %v", got, err)
			}
			partial, err := hashing.PartialMD5(path)
			if err != nil {
				t.Fatal(err)
			}
			if n := e.count(`SELECT count(*) FROM library_file_hashes WHERE hash = '` + partial + `'`); n != 1 {
				t.Fatalf("partial MD5 is not indexed: %d", n)
			}
			after, err := os.ReadFile(filepath.Join(e.root, tt.file))
			if err != nil || !bytes.Equal(after, tt.content) {
				t.Fatalf("source changed: %v", err)
			}
			e.tmpClean()
		})
	}
}

func TestConvertBroken(t *testing.T) {
	for name, content := range map[string]string{
		"garbage.fb2": "this is not xml at all", "cut.fb2": `<?xml version="1.0"?><FictionBook><description>`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			src := e.add(name, []byte(content))
			if _, err := e.service().Convert(e.ctx, src); !errors.Is(err, ErrFailed) {
				t.Fatalf("%v", err)
			}
			e.noResult()
		})
	}
}

func TestConvertNotFB2(t *testing.T) {
	e := newEnv(t)
	src := e.add("a.pdf", []byte("pdf"))
	if _, err := e.service().Convert(e.ctx, src); !errors.Is(err, ErrNotConvertible) {
		t.Fatalf("%v", err)
	}
	if _, err := e.service().Convert(e.ctx, 9999); !errors.Is(err, library.ErrFileNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestConvertSourceGone(t *testing.T) {
	e := newEnv(t)
	src := e.add("a.fb2", []byte(fb2Doc("utf-8", "")))
	if err := os.Remove(filepath.Join(e.root, "a.fb2")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service().Convert(e.ctx, src); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	e.noResult()
}

func TestConvertChildFailures(t *testing.T) {
	for name, body := range map[string]string{
		// Паника чужой горутины завершает процесс Go с кодом 2.
		"panic":  "exit 2",
		"signal": "kill -9 $$",
		// Код 0 без файла результата.
		"no output": "exit 0",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			src := e.add("a.fb2", []byte(fb2Doc("utf-8", "")))
			s := e.service(WithExecutable(e.script(body)))
			if _, err := s.Convert(e.ctx, src); !errors.Is(err, ErrFailed) {
				t.Fatalf("%v", err)
			}
			e.noResult()
		})
	}
}

func TestConvertExitCodes(t *testing.T) {
	e := newEnv(t)
	src := e.add("a.fb2", []byte(fb2Doc("utf-8", "")))
	if _, err := e.service(WithExecutable(e.script("exit 3"))).Convert(e.ctx, src); !errors.Is(err, meta.ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	if _, err := e.service(WithExecutable(e.script("exit 4"))).Convert(e.ctx, src); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable: %v", err)
	}
}

func TestConvertTimeout(t *testing.T) {
	e := newEnv(t)
	src := e.add("a.fb2", []byte(fb2Doc("utf-8", "")))
	s := e.service(WithExecutable(e.script("exec sleep 30")), WithTimeout(100*time.Millisecond))
	start := time.Now()
	if _, err := s.Convert(e.ctx, src); !errors.Is(err, ErrTimeout) {
		t.Fatalf("%v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
	e.noResult()
}

// Срок может выйти и после запуска, на обращении к базе.
func TestConvertExpiredBeforeStart(t *testing.T) {
	e := newEnv(t)
	src := e.add("a.fb2", []byte(fb2Doc("utf-8", "")))
	s := e.service(WithTimeout(0))
	for range 20 {
		if _, err := s.Convert(e.ctx, src); !errors.Is(err, ErrTimeout) {
			t.Fatalf("%v", err)
		}
	}
}

// counting — настоящий конвертер, считающий запуски в файл и ждущий delay секунд.
func (e *testEnv) counting(delay string) (exe, counter string) {
	e.t.Helper()
	counter = filepath.Join(e.t.TempDir(), "runs")
	return e.script(fmt.Sprintf("echo x >> %q\nsleep %s\nexec %q \"$@\"", counter, delay, self(e.t))), counter
}

func runs(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "x")
}

func TestConvertConcurrent(t *testing.T) {
	e := newEnv(t)
	src := e.add("a.fb2", []byte(fb2Doc("utf-8", "")))
	exe, counter := e.counting("0.2")
	s := e.service(WithExecutable(exe))
	ids := make([]int64, 4)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			id, err := s.Convert(e.ctx, src)
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id == 0 || id != ids[0] {
			t.Fatalf("ids %v", ids)
		}
	}
	if n := runs(t, counter); n != 1 {
		t.Fatalf("%d runs", n)
	}
	// Последовательный запрос получает готовый файл.
	before, err := os.Stat(e.result(ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.Convert(e.ctx, src); err != nil || id != ids[0] {
		t.Fatalf("repeat: %d, %v", id, err)
	}
	after, err := os.Stat(e.result(ids[0]))
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("result touched: %v", err)
	}
	if n := runs(t, counter); n != 1 {
		t.Fatalf("%d runs after repeat", n)
	}
}

func TestConvertSameContentDifferentFiles(t *testing.T) {
	e := newEnv(t)
	content := []byte(fb2Doc("utf-8", ""))
	a := e.add("a.fb2", content)
	b := e.add("b.fb2", content)
	s := e.service()
	first, err := s.Convert(e.ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := s.Convert(e.ctx, b); err != nil || second != first {
		t.Fatalf("second copy: %d, %v", second, err)
	}
}

func TestConvertSurvivesClientDisconnect(t *testing.T) {
	e := newEnv(t)
	src := e.add("a.fb2", []byte(fb2Doc("utf-8", "")))
	exe, counter := e.counting("0.3")
	s := e.service(WithExecutable(exe))
	ctx, cancel := context.WithTimeout(e.ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := s.Convert(ctx, src); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for e.count(`SELECT count(*) FROM library_files WHERE source_md5 IS NOT NULL`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("result did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := s.Convert(e.ctx, src); err != nil {
		t.Fatal(err)
	}
	if n := runs(t, counter); n != 1 {
		t.Fatalf("%d runs", n)
	}
}

func TestConvertShutdownKillsChild(t *testing.T) {
	e := newEnv(t)
	src := e.add("a.fb2", []byte(fb2Doc("utf-8", "")))
	ctx, stop := context.WithCancel(e.ctx)
	s, err := New(ctx, e.st, e.dataDir, slog.New(slog.DiscardHandler), WithExecutable(e.script("exec sleep 30")))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Convert(e.ctx, src); done <- err }()
	time.Sleep(100 * time.Millisecond)
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child was not killed")
	}
	s.Wait(5 * time.Second)
	e.noResult()
}

// Файлы рядом с рабочим каталогом дочернего процесса не должны попасть в EPUB.
func TestConvertStylesheetCannotReadFiles(t *testing.T) {
	const marker = "SECRET-MARKER-7f3a"
	e := newEnv(t)
	css := `<stylesheet type="text/css">@font-face { font-family: x; src: url(secret.ttf); }
.a { background: url(../secret.ttf); } .b { background: url(secret.png); }</stylesheet>`
	src := e.add("a.fb2", []byte(fb2Doc("utf-8", css)))
	// Секрет лежит в рабочем каталоге процесса и в TMPDIR: базе url(...), если бы ею стал один из них.
	exe := e.script(fmt.Sprintf(`for d in "$PWD" "$TMPDIR" "$PWD/.."; do for f in secret.ttf secret.png; do echo %s > "$d/$f"; done; done
exec %q "$@"`, marker, self(t)))
	id, err := e.service(WithExecutable(exe)).Convert(e.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(epubText(t, e.result(id)), marker) {
		t.Fatal("file from disk leaked into EPUB")
	}
}

func TestConvertLongName(t *testing.T) {
	e := newEnv(t)
	// 255 байт вместе с .fb2: кириллица по 2 байта плюс один ASCII для чётности.
	stem := "a" + strings.Repeat("я", 125)
	name := stem + ".fb2"
	if len(name) != 255 {
		t.Fatalf("name is %d bytes", len(name))
	}
	src := e.add(name, []byte(fb2Doc("utf-8", "")))
	id, err := e.service().Convert(e.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(e.result(id)); got != "a"+strings.Repeat("я", 124)+".epub" {
		t.Fatalf("name %q (%d bytes)", got, len(got))
	}
}

func TestResultName(t *testing.T) {
	for _, tt := range []struct{ rel, format, want string }{
		{"sub/Пикник.fb2", "fb2", "Пикник.epub"},
		{"Пикник.fb2.zip", "fb2.zip", "Пикник.epub"},
		{"a.b.FB2", "fb2", "a.b.epub"},
	} {
		if got := ResultName(tt.rel, tt.format); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.rel, got, tt.want)
		}
	}
}

func TestDelete(t *testing.T) {
	e := newEnv(t)
	src := e.add("a.fb2", []byte(fb2Doc("utf-8", "")))
	s := e.service()
	id, err := s.Convert(e.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(e.ctx, src); !errors.Is(err, library.ErrNotConverted) {
		t.Fatalf("regular file: %v", err)
	}
	if err := s.Delete(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	e.noResult()
	if _, err := s.Convert(e.ctx, src); err != nil {
		t.Fatalf("convert again: %v", err)
	}
}

func TestNewCleansTmpAndMakesPathAbsolute(t *testing.T) {
	e := newEnv(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	stale := filepath.Join(base, "data", "tmp", "job-old", "book.epub.tmp")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(e.ctx, e.st, "data", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file: %v", err)
	}
	if !filepath.IsAbs(s.converted) || !filepath.IsAbs(s.temp) {
		t.Fatalf("paths %q %q", s.converted, s.temp)
	}
}
