package apiv1

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/library/convert"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
)

// script создаёт программу дочернего процесса из текста shell. Настоящий конвертер
// проверяется в пакете convert, здесь важны ответы API.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeConverter кладёт готовый EPUB в файл результата; для имён с broken падает.
func fakeConverter(t *testing.T) string {
	t.Helper()
	epub, err := filepath.Abs("../library/meta/testdata/epub2.epub")
	if err != nil {
		t.Fatal(err)
	}
	return script(t, fmt.Sprintf(`case "$3" in
*broken*) echo "secret detail from converter" >&2; exit 1;;
esac
cp %q "$5"`, epub))
}

func (e *env) useConverter(exe string, opts ...convert.Option) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.ctx)
	log := slog.New(slog.NewJSONHandler(e.logs, nil))
	c, err := convert.New(ctx, e.srv.Library.Store, e.dataDir, log, append([]convert.Option{convert.WithExecutable(exe)}, opts...)...)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { cancel(); c.Wait(10 * time.Second) })
	e.srv.Library.Converter = c
}

// bookFiles — файлы книги из ответа API.
func bookFiles(t *testing.T, b *browser, book int64) []map[string]any {
	t.Helper()
	r := b.do("GET", fmt.Sprintf("/api/v1/library/books/%d", book), nil)
	r.expect(t, "book", 200, "")
	out := []map[string]any{}
	for _, f := range r.body["files"].([]any) {
		out = append(out, f.(map[string]any))
	}
	return out
}

func convertURL(id int64) string { return fmt.Sprintf("/api/v1/library/files/%d/convert", id) }

func TestLibraryConvert(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	dir := libraryFixture(t, admin, map[string]string{"Пикник.fb2": "fb2 source", "Другой.fb2": "other"})
	user := e.panelUser("alice", accounts.RoleUser)
	book := libraryIDs(t, user)["Пикник.fb2"]
	source := fileIDs(t, user, book)[0]

	r := user.do("POST", convertURL(source), nil)
	r.expect(t, "convert", 200, "")
	id := int64(r.body["file_id"].(float64))

	files := bookFiles(t, user, book)
	if len(files) != 2 {
		t.Fatalf("files %v", files)
	}
	if files[0]["convertible"] != false || files[0]["converted"] != false {
		t.Fatalf("source %v", files[0])
	}
	if int64(files[1]["id"].(float64)) != id || files[1]["format"] != "epub" || files[1]["converted"] != true ||
		files[1]["convertible"] != false || files[1]["pinned"] != true {
		t.Fatalf("result %v", files[1])
	}
	if !strings.HasSuffix(files[1]["path"].(string), "/Пикник.epub") {
		t.Fatalf("path %v", files[1]["path"])
	}
	other := libraryIDs(t, user)["Другой.fb2"]
	if f := bookFiles(t, user, other)[0]; f["convertible"] != true {
		t.Fatalf("other %v", f)
	}

	content := user.raw("GET", fmt.Sprintf("/api/v1/library/files/%d/content", id), nil)
	want, err := os.ReadFile("../library/meta/testdata/epub2.epub")
	if err != nil {
		t.Fatal(err)
	}
	if content.status != 200 || content.body != string(want) || content.header.Get("Content-Type") != "application/epub+zip" {
		t.Fatalf("download: %d %v", content.status, content.header)
	}
	_, params, err := mime.ParseMediaType(content.header.Get("Content-Disposition"))
	if err != nil || params["filename"] != "Пикник.epub" {
		t.Fatalf("disposition %q, %v", content.header.Get("Content-Disposition"), err)
	}

	// Повторный запрос — тот же файл.
	again := user.do("POST", convertURL(source), nil)
	again.expect(t, "repeat", 200, "")
	if int64(again.body["file_id"].(float64)) != id {
		t.Fatalf("repeat %v, want %d", again.body, id)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "Пикник.fb2")); err != nil || string(got) != "fb2 source" {
		t.Fatalf("source: %q, %v", got, err)
	}

	// Служебный корень не виден и не считается.
	roots := admin.do("GET", "/api/v1/library/roots", nil)
	roots.expect(t, "roots", 200, "")
	if items, _ := roots.body["items"].([]any); len(items) != 1 {
		t.Fatalf("roots %v", roots.body)
	}
	sum := admin.do("GET", "/api/v1/library", nil)
	if sum.body["roots"] != float64(1) || sum.body["files"] != float64(3) {
		t.Fatalf("summary %v", sum.body)
	}
}

func TestLibraryConvertErrors(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	dir := libraryFixture(t, admin, map[string]string{
		"a.pdf": "pdf", "broken.fb2": "bad", "gone.fb2": "gone",
		"big.fb2": "big one", "killed.fb2": "kill me", "slow.fb2": "slow one",
	})
	ids := libraryIDs(t, admin)
	file := func(name string) int64 { return fileIDs(t, admin, ids[name])[0] }

	admin.do("POST", convertURL(file("a.pdf")), nil).expect(t, "pdf", 409, "not_convertible")
	admin.do("POST", convertURL(9999), nil).expect(t, "missing", 404, "not_found")

	r := admin.do("POST", convertURL(file("broken.fb2")), nil)
	r.expect(t, "broken", 422, "conversion_failed")
	if strings.Contains(fmt.Sprint(r.body), "secret detail") {
		t.Fatalf("converter output leaked: %v", r.body)
	}
	if !strings.Contains(e.logs.String(), "secret detail") {
		t.Fatal("converter output is not logged")
	}

	gone := file("gone.fb2")
	if err := os.Remove(filepath.Join(dir, "gone.fb2")); err != nil {
		t.Fatal(err)
	}
	e.useConverter(script(t, "exit 4"))
	admin.do("POST", convertURL(gone), nil).expect(t, "gone", 404, "file_unavailable")

	e.useConverter(script(t, "exit 3"))
	admin.do("POST", convertURL(file("big.fb2")), nil).expect(t, "big", 413, "file_too_large")

	// Убитый дочерний процесс не мешает серверу.
	e.useConverter(script(t, "kill -9 $$"))
	admin.do("POST", convertURL(file("killed.fb2")), nil).expect(t, "killed", 422, "conversion_failed")
	admin.do("GET", "/api/v1/library", nil).expect(t, "still alive", 200, "")

	e.useConverter(script(t, "exec sleep 30"), convert.WithTimeout(100*time.Millisecond))
	admin.do("POST", convertURL(file("slow.fb2")), nil).expect(t, "slow", 504, "conversion_timeout")

	entries, err := os.ReadDir(filepath.Join(e.dataDir, "converted"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("converted dir: %v, %v", entries, err)
	}
	if n := e.count(`SELECT count(*) FROM library_files WHERE source_md5 IS NOT NULL`); n != 0 {
		t.Fatalf("%d results in the index", n)
	}
}

func TestLibraryConvertReadOnlyRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	dir := libraryFixture(t, admin, map[string]string{"a.fb2": "fb2"})
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	admin.do("POST", convertURL(fileIDs(t, admin, libraryIDs(t, admin)["a.fb2"])[0]), nil).expect(t, "convert", 200, "")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("root: %v, %v", entries, err)
	}
}

func TestLibraryDeleteConverted(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"a.fb2": "fb2"})
	user := e.panelUser("alice", accounts.RoleUser)
	book := libraryIDs(t, admin)["a.fb2"]
	source := fileIDs(t, admin, book)[0]
	r := admin.do("POST", convertURL(source), nil)
	r.expect(t, "convert", 200, "")
	id := int64(r.body["file_id"].(float64))
	url := fmt.Sprintf("/api/v1/library/files/%d", id)

	var hash string
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT partial_md5 FROM library_files WHERE id = ?`, id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	_, pass, err := e.acc.AddDevice(e.ctx, "alice", "reader")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := e.acc.Authenticate(e.ctx, "alice", accounts.KeyFromPassword(pass), netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := progress.New(e.db).Save(e.ctx, dev, progress.Update{
		Document: hash, Percentage: 0.4, Progress: "x", Device: "d", ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	user.do("DELETE", url, nil).expect(t, "not admin", 403, "forbidden")
	admin.do("DELETE", fmt.Sprintf("/api/v1/library/files/%d", source), nil).expect(t, "regular", 409, "not_converted")
	admin.do("DELETE", "/api/v1/library/files/9999", nil).expect(t, "missing", 404, "not_found")
	admin.do("DELETE", url, nil).expect(t, "delete", 204, "")

	files := bookFiles(t, admin, book)
	if len(files) != 1 || files[0]["convertible"] != true {
		t.Fatalf("files %v", files)
	}
	entries, err := os.ReadDir(filepath.Join(e.dataDir, "converted"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("converted dir: %v, %v", entries, err)
	}
	if n := e.count(`SELECT count(*) FROM progress_current`); n != 1 {
		t.Fatalf("%d progress rows", n)
	}
	admin.do("POST", convertURL(source), nil).expect(t, "convert again", 200, "")
}

func TestLibraryConvertDataDirMoved(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"a.fb2": "fb2"})
	r := admin.do("POST", convertURL(fileIDs(t, admin, libraryIDs(t, admin)["a.fb2"])[0]), nil)
	r.expect(t, "convert", 200, "")
	id := int64(r.body["file_id"].(float64))

	moved := t.TempDir()
	if err := os.Rename(filepath.Join(e.dataDir, "converted"), filepath.Join(moved, "converted")); err != nil {
		t.Fatal(err)
	}
	e.dataDir = moved
	e.useConverter(fakeConverter(t))
	if got := admin.raw("GET", fmt.Sprintf("/api/v1/library/files/%d/content", id), nil); got.status != 200 {
		t.Fatalf("download after move: %d", got.status)
	}
}

func TestLibraryRootOverlapsDataDir(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	admin.do("POST", "/api/v1/library/roots", map[string]string{"path": e.dataDir}).expect(t, "data dir", 409, "root_overlap")
}

func TestLibraryConvertOutlivesServerTimeouts(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"a.fb2": "fb2"})
	source := fileIDs(t, admin, libraryIDs(t, admin)["a.fb2"])[0]
	epub, err := filepath.Abs("../library/meta/testdata/epub2.epub")
	if err != nil {
		t.Fatal(err)
	}
	e.useConverter(script(t, fmt.Sprintf(`sleep 0.5
cp %q "$5"`, epub)))

	slow := httptest.NewUnstartedServer(NewHandler(e.srv))
	slow.Config.ReadTimeout = 100 * time.Millisecond
	slow.Config.WriteTimeout = 100 * time.Millisecond
	slow.Start()
	t.Cleanup(slow.Close)
	e.url = slow.URL

	r := admin.do("POST", convertURL(source), nil)
	r.expect(t, "convert", 200, "")
	if r.body["file_id"] == nil {
		t.Fatalf("empty response: %v", r.body)
	}
}

// Остановка сервера отменяет задачу, а клиент ещё ждёт: пустой 200 выглядел бы успехом.
func TestLibraryConvertServerShutdown(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"a.fb2": "fb2"})
	source := fileIDs(t, admin, libraryIDs(t, admin)["a.fb2"])[0]

	ctx, stop := context.WithCancel(e.ctx)
	defer stop()
	c, err := convert.New(ctx, e.srv.Library.Store, e.dataDir, slog.New(slog.DiscardHandler),
		convert.WithExecutable(script(t, "exec sleep 30")))
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Library.Converter = c
	time.AfterFunc(100*time.Millisecond, stop)
	admin.do("POST", convertURL(source), nil).expect(t, "shutdown", 500, "internal")
	c.Wait(5 * time.Second)
}
