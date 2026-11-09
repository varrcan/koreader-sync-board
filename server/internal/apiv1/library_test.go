package apiv1

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library"
)

// waitScan ждёт, пока сводка библиотеки покажет законченный скан.
func waitScan(t *testing.T, b *browser) resp {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := b.do("GET", "/api/v1/library", nil)
		r.expect(t, "summary", 200, "")
		if scan, _ := r.body["scan"].(map[string]any); scan["running"] == false {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLibraryAccess(t *testing.T) {
	e := newEnv(t)
	anon := e.browser()
	for _, c := range [][2]string{{"GET", "/api/v1/library"}, {"GET", "/api/v1/library/roots"},
		{"POST", "/api/v1/library/roots"}, {"DELETE", "/api/v1/library/roots/1"}, {"POST", "/api/v1/library/scan"}} {
		anon.do(c[0], c[1], map[string]string{"path": "/"}).expect(t, "anon "+c[1], 401, "unauthorized")
	}

	user := e.panelUser("alice", accounts.RoleUser)
	r := user.do("GET", "/api/v1/library", nil)
	r.expect(t, "user summary", 200, "")
	scan, _ := r.body["scan"].(map[string]any)
	if r.body["books"] != 0.0 || r.body["files"] != 0.0 || r.body["roots"] != 0.0 ||
		scan["running"] != false || scan["finished_at"] != nil || scan["started_at"] != nil {
		t.Fatalf("empty summary %v", r.body)
	}
	for _, c := range [][2]string{{"GET", "/api/v1/library/roots"}, {"POST", "/api/v1/library/roots"},
		{"DELETE", "/api/v1/library/roots/1"}, {"POST", "/api/v1/library/scan"}} {
		user.do(c[0], c[1], map[string]string{"path": t.TempDir()}).expect(t, "user "+c[1], 403, "forbidden")
	}
	if n := e.count(`SELECT count(*) FROM library_roots WHERE managed = 0`); n != 0 {
		t.Fatalf("user created %d roots", n)
	}
}

func TestLibraryRoots(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	base := t.TempDir()
	books := filepath.Join(base, "books")
	if err := os.MkdirAll(filepath.Join(books, "fantasy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "fantasy", "a.pdf"), []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(books, "fantasy"), link); err != nil {
		t.Fatal(err)
	}

	r := admin.do("POST", "/api/v1/library/roots", map[string]string{"path": books})
	r.expect(t, "add", 201, "")
	if r.body["name"] != "books" || r.body["path"] != books || r.body["files"] != 0.0 ||
		r.body["scanned_at"] != nil || r.body["error"] != nil {
		t.Fatalf("root %v", r.body)
	}
	id := r.body["id"]
	// Добавление запустило скан.
	sum := waitScan(t, admin)
	if sum.body["files"] != 1.0 || sum.body["books"] != 1.0 || sum.body["roots"] != 1.0 {
		t.Fatalf("summary after add %v", sum.body)
	}

	cases := []struct{ path, code string }{
		{"books", "invalid_path"},
		{filepath.Join(books, "fantasy", "a.pdf"), "invalid_path"},
		{filepath.Join(base, "missing"), "invalid_path"},
		{books + "/", "root_exists"},
		{filepath.Join(books, "fantasy"), "root_overlap"},
		{base, "root_overlap"},
		{link, "root_overlap"},
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(t.TempDir(), "locked")
		if err := os.Mkdir(locked, 0); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, struct{ path, code string }{locked, "invalid_path"})
	}
	for _, c := range cases {
		status := 409
		if c.code == "invalid_path" {
			status = 400
		}
		admin.do("POST", "/api/v1/library/roots", map[string]string{"path": c.path}).expect(t, c.path, status, c.code)
	}
	admin.do("POST", "/api/v1/library/roots", nil).expect(t, "no body", 400, "bad_request")

	other := t.TempDir()
	r = admin.do("POST", "/api/v1/library/roots", map[string]string{"path": other, "name": "Мои книги"})
	r.expect(t, "named", 201, "")
	if r.body["name"] != "Мои книги" {
		t.Fatalf("name %v", r.body["name"])
	}
	waitScan(t, admin)

	list := admin.do("GET", "/api/v1/library/roots", nil)
	list.expect(t, "list", 200, "")
	items, _ := list.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items %v", list.body)
	}
	first, _ := items[0].(map[string]any)
	if first["files"] != 1.0 || first["scanned_at"] == nil || first["error"] != nil {
		t.Fatalf("first root %v", first)
	}

	rootPath := fmt.Sprintf("/api/v1/library/roots/%.0f", id)
	admin.do("DELETE", rootPath, nil).expect(t, "delete", 204, "")
	admin.do("DELETE", rootPath, nil).expect(t, "delete again", 404, "not_found")
	sum = admin.do("GET", "/api/v1/library", nil)
	if sum.body["files"] != 0.0 || sum.body["books"] != 0.0 || sum.body["roots"] != 1.0 {
		t.Fatalf("summary after delete %v", sum.body)
	}
	if _, err := os.Stat(filepath.Join(books, "fantasy", "a.pdf")); err != nil {
		t.Fatal("file on disk touched")
	}
}

func TestLibraryScan(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	dir := t.TempDir()
	admin.do("POST", "/api/v1/library/roots", map[string]string{"path": dir}).expect(t, "add", 201, "")
	waitScan(t, admin)
	if err := os.WriteFile(filepath.Join(dir, "b.epub"), []byte("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := admin.do("POST", "/api/v1/library/scan", nil)
	r.expect(t, "scan", 202, "")
	if scan, _ := r.body["scan"].(map[string]any); scan["running"] != true {
		t.Fatalf("scan response %v", r.body)
	}
	sum := waitScan(t, admin)
	if scan, _ := sum.body["scan"].(map[string]any); sum.body["files"] != 1.0 || scan["finished_at"] == nil {
		t.Fatalf("summary %v", sum.body)
	}
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read.QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// libraryFixture создаёт корень с файлами names и дожидается скана. Файлы без
// разбираемых метаданных становятся отдельными книгами.
func libraryFixture(t *testing.T, admin *browser, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	admin.do("POST", "/api/v1/library/roots", map[string]string{"path": dir}).expect(t, "add root", 201, "")
	waitScan(t, admin)
	return dir
}

// libraryIDs — id книг каталога по имени файла.
func libraryIDs(t *testing.T, b *browser) map[string]int64 {
	t.Helper()
	r := b.do("GET", "/api/v1/library/books", nil)
	r.expect(t, "catalog", 200, "")
	items, _ := r.body["items"].([]any)
	out := map[string]int64{}
	for _, it := range items {
		m := it.(map[string]any)
		out[m["filename"].(string)] = int64(m["id"].(float64))
	}
	return out
}

// fileIDs — id файлов книги по порядку.
func fileIDs(t *testing.T, b *browser, book int64) []int64 {
	t.Helper()
	r := b.do("GET", fmt.Sprintf("/api/v1/library/books/%d", book), nil)
	r.expect(t, "book", 200, "")
	files, _ := r.body["files"].([]any)
	out := make([]int64, 0, len(files))
	for _, f := range files {
		out = append(out, int64(f.(map[string]any)["id"].(float64)))
	}
	return out
}

func TestLibraryBooksAccess(t *testing.T) {
	e := newEnv(t)
	anon := e.browser()
	for _, c := range [][2]string{{"GET", "/api/v1/library/books"}, {"GET", "/api/v1/library/books/1"},
		{"GET", "/api/v1/library/files/1/content"}, {"POST", "/api/v1/library/books/1/merge"},
		{"POST", "/api/v1/library/files/1/split"}} {
		anon.do(c[0], c[1], map[string]int{"book_id": 2}).expect(t, "anon "+c[1], 401, "unauthorized")
	}

	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"sub/a.pdf": "a", "b.pdf": "bb"})
	user := e.panelUser("alice", accounts.RoleUser)

	r := user.do("GET", "/api/v1/library/books?limit=1", nil)
	r.expect(t, "first page", 200, "")
	items, _ := r.body["items"].([]any)
	first := items[0].(map[string]any)
	if len(items) != 1 || first["filename"] != "a.pdf" || first["title"] != nil || first["files"] != 1.0 ||
		fmt.Sprint(first["formats"]) != "[pdf]" {
		t.Fatalf("page %v", r.body)
	}
	cursor, _ := r.body["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("no cursor: %v", r.body)
	}
	r = user.do("GET", "/api/v1/library/books?limit=1&cursor="+cursor, nil)
	r.expect(t, "second page", 200, "")
	if items, _ := r.body["items"].([]any); len(items) != 1 || r.body["next_cursor"] != nil {
		t.Fatalf("second page %v", r.body)
	}
	r = user.do("GET", "/api/v1/library/books?q="+url.QueryEscape("SUB"), nil)
	if items, _ := r.body["items"].([]any); len(items) != 1 {
		t.Fatalf("search %v", r.body)
	}

	for _, q := range []string{"limit=0", "limit=101", "limit=x", "cursor=x", "cursor=0",
		"q=" + strings.Repeat("я", 201)} {
		user.do("GET", "/api/v1/library/books?"+q, nil).expect(t, q, 400, "bad_request")
	}
	user.do("GET", "/api/v1/library/books?q="+strings.Repeat("я", 200), nil).expect(t, "max q", 200, "")

	ids := libraryIDs(t, user)
	r = user.do("GET", fmt.Sprintf("/api/v1/library/books/%d", ids["a.pdf"]), nil)
	r.expect(t, "book", 200, "")
	files, _ := r.body["files"].([]any)
	file, _ := files[0].(map[string]any)
	if file["path"] != "sub/a.pdf" || file["format"] != "pdf" || file["size"] != 1.0 || file["pinned"] != false ||
		file["progress"] != nil || file["root_name"] == "" || file["root"] != nil {
		t.Fatalf("file %v", file)
	}
	user.do("GET", "/api/v1/library/books/9999", nil).expect(t, "missing", 404, "not_found")
	user.do("GET", "/api/v1/library/books/x", nil).expect(t, "bad id", 400, "bad_request")
}

func TestLibraryBookProgress(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	dir := libraryFixture(t, admin, map[string]string{"a.pdf": "content"})
	partial, err := hashing.PartialMD5(filepath.Join(dir, "a.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	user := e.panelUser("alice", accounts.RoleUser)
	e.reader("alice", "pb").sync(partial, 0.4, t0, "")
	e.reader("root", "pb").sync(partial, 0.9, t0, "")

	id := libraryIDs(t, user)["a.pdf"]
	r := user.do("GET", fmt.Sprintf("/api/v1/library/books/%d", id), nil)
	files, _ := r.body["files"].([]any)
	p, _ := files[0].(map[string]any)["progress"].(map[string]any)
	if p["percentage"] != 0.4 || p["hash"] != partial || p["finished"] != false || p["updated_at"] == nil {
		t.Fatalf("progress %v", p)
	}
	r = user.do("GET", "/api/v1/books/"+partial, nil)
	r.expect(t, "progress card", 200, "")
	book, _ := r.body["book"].(map[string]any)
	if book == nil {
		book = r.body
	}
	if book["library_book_id"] != float64(id) {
		t.Fatalf("library_book_id %v", r.body)
	}
}

func TestLibraryMergeSplit(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"a.pdf": "a", "b.pdf": "bb"})
	user := e.panelUser("alice", accounts.RoleUser)
	ids := libraryIDs(t, admin)
	a, b := ids["a.pdf"], ids["b.pdf"]
	merge := fmt.Sprintf("/api/v1/library/books/%d/merge", a)

	user.do("POST", merge, map[string]int64{"book_id": b}).expect(t, "user merge", 403, "forbidden")
	if n := e.count(`SELECT count(*) FROM library_books`); n != 2 {
		t.Fatalf("user changed books: %d", n)
	}
	admin.do("POST", merge, map[string]int64{"book_id": a}).expect(t, "self", 400, "bad_request")
	admin.do("POST", merge, nil).expect(t, "no body", 400, "bad_request")
	admin.do("POST", merge, map[string]int64{"book_id": 9999}).expect(t, "no source", 404, "not_found")
	admin.do("POST", "/api/v1/library/books/9999/merge", map[string]int64{"book_id": b}).expect(t, "no target", 404, "not_found")

	r := admin.do("POST", merge, map[string]int64{"book_id": b})
	r.expect(t, "merge", 200, "")
	files, _ := r.body["files"].([]any)
	if len(files) != 2 || files[1].(map[string]any)["pinned"] != true {
		t.Fatalf("merged %v", r.body)
	}
	if !strings.Contains(e.logs.String(), "panel: library books merged") {
		t.Fatal("merge not logged")
	}

	fs := fileIDs(t, admin, a)
	split := fmt.Sprintf("/api/v1/library/files/%d/split", fs[1])
	user.do("POST", split, nil).expect(t, "user split", 403, "forbidden")
	admin.do("POST", "/api/v1/library/files/9999/split", nil).expect(t, "no file", 404, "not_found")
	r = admin.do("POST", split, nil)
	r.expect(t, "split", 201, "")
	book, _ := r.body["book"].(map[string]any)
	if book["id"] == float64(a) || book["files"] != 1.0 {
		t.Fatalf("split %v", r.body)
	}
	if !strings.Contains(e.logs.String(), "panel: library file split") {
		t.Fatal("split not logged")
	}
	admin.do("POST", fmt.Sprintf("/api/v1/library/files/%d/split", fs[0]), nil).expect(t, "single", 409, "single_file")
}

type rawResp struct {
	status int
	header http.Header
	body   string
}

func (b *browser) raw(method, path string, header map[string]string) rawResp {
	b.e.t.Helper()
	req, _ := http.NewRequest(method, b.e.url+path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		b.e.t.Fatal(err)
	}
	return rawResp{status: res.StatusCode, header: res.Header, body: string(data)}
}

func TestLibraryDownloadConverted(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"Пикник.fb2": "fb2"})
	book := libraryIDs(t, admin)["Пикник.fb2"]
	source := fileIDs(t, admin, book)[0]

	st := library.NewStore(e.db)
	managed := filepath.Join(t.TempDir(), "converted")
	if err := st.EnsureManagedRoot(e.ctx, managed); err != nil {
		t.Fatal(err)
	}
	const name = "Пикник.epub"
	content := strings.Repeat("epub bytes", 500)
	file := filepath.Join(managed, "abc", name)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := st.AddConverted(e.ctx, library.Converted{SourceID: source, SourceMD5: "abc", Path: "abc/" + name,
		Size: int64(len(content)), MTime: 1, PartialMD5: "abc"})
	if err != nil {
		t.Fatal(err)
	}

	r := admin.raw("GET", fmt.Sprintf("/api/v1/library/files/%d/content", id), nil)
	if r.status != 200 || r.body != content || r.header.Get("Content-Type") != "application/epub+zip" {
		t.Fatalf("download: %d %v, %d bytes", r.status, r.header, len(r.body))
	}
	_, params, err := mime.ParseMediaType(r.header.Get("Content-Disposition"))
	if err != nil || params["filename"] != name {
		t.Fatalf("disposition %q: %v", r.header.Get("Content-Disposition"), err)
	}
}

func TestLibraryDownload(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	content := strings.Repeat("0123456789", 100)
	dir := libraryFixture(t, admin, map[string]string{
		"sub/Пикник.epub": content, "a b.pdf": "pdf", "gone.pdf": "gone!", "evil.pdf": "evil!!", "x.fb2.zip": "zipped",
	})
	user := e.panelUser("alice", accounts.RoleUser)
	fileURL := func(book string) string {
		t.Helper()
		return fmt.Sprintf("/api/v1/library/files/%d/content", fileIDs(t, user, libraryIDs(t, user)[book])[0])
	}
	epub := fileURL("Пикник.epub")

	r := user.raw("GET", epub, nil)
	if r.status != 200 || r.body != content || r.header.Get("Content-Type") != "application/epub+zip" ||
		r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %v", r.status, r.header)
	}
	if got, want := r.header.Get("Content-Disposition"),
		"attachment; filename*=utf-8''%D0%9F%D0%B8%D0%BA%D0%BD%D0%B8%D0%BA.epub"; got != want {
		t.Fatalf("disposition %q, want %q", got, want)
	}
	if got := user.raw("GET", fileURL("a b.pdf"), nil).header.Get("Content-Disposition"); got != `attachment; filename="a b.pdf"` {
		t.Fatalf("ascii disposition %q", got)
	}
	if got := user.raw("GET", fileURL("x.fb2.zip"), nil).header.Get("Content-Type"); got != "application/zip" {
		t.Fatalf("fb2.zip type %q", got)
	}

	r = user.raw("GET", epub, map[string]string{"Range": "bytes=100-199"})
	if r.status != 206 || r.body != content[100:200] {
		t.Fatalf("range: %d %d bytes", r.status, len(r.body))
	}
	if r := user.raw("GET", epub, map[string]string{"Range": "bytes=5000-"}); r.status != 416 {
		t.Fatalf("range past end: %d", r.status)
	}
	r = user.raw("HEAD", epub, nil)
	if r.status != 200 || r.body != "" || r.header.Get("Content-Length") != "1000" ||
		!strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("head: %d %q %v", r.status, r.body, r.header)
	}

	user.do("GET", "/api/v1/library/files/9999/content", nil).expect(t, "not indexed", 404, "not_found")

	gone := fileURL("gone.pdf")
	if err := os.Remove(filepath.Join(dir, "gone.pdf")); err != nil {
		t.Fatal(err)
	}
	user.do("GET", gone, nil).expect(t, "removed", 404, "file_unavailable")
	if r := user.raw("HEAD", gone, nil); r.status != 404 {
		t.Fatalf("head of removed: %d", r.status)
	}
	if !strings.Contains(e.logs.String(), `"root":"`+dir) || !strings.Contains(e.logs.String(), `"path":"gone.pdf"`) {
		t.Fatalf("unavailable not logged:\n%s", e.logs)
	}

	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	evil := fileURL("evil.pdf")
	path := filepath.Join(dir, "evil.pdf")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, path); err != nil {
		t.Fatal(err)
	}
	r2 := user.raw("GET", evil, nil)
	if r2.status != 404 || strings.Contains(r2.body, "top secret") || !strings.Contains(r2.body, "file_unavailable") {
		t.Fatalf("symlink out of root: %d %q", r2.status, r2.body)
	}
}

// Скачивание дольше общих сроков сервера: клиент читает медленно, и запись упирается
// в заполненный буфер сокета уже после истечения WriteTimeout.
func TestLibraryDownloadOutlivesServerTimeouts(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	dir := libraryFixture(t, admin, map[string]string{"big.pdf": "x"})
	const size = 64 << 20
	if err := os.Truncate(filepath.Join(dir, "big.pdf"), size); err != nil {
		t.Fatal(err)
	}
	id := fileIDs(t, admin, libraryIDs(t, admin)["big.pdf"])[0]

	srv := httptest.NewUnstartedServer(NewHandler(e.srv))
	srv.Config.ReadTimeout = 100 * time.Millisecond
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	defer srv.Close()

	res, err := admin.c.Get(fmt.Sprintf("%s/api/v1/library/files/%d/content", srv.URL, id))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	head := make([]byte, 1024)
	if _, err := io.ReadFull(res.Body, head); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	n, err := io.Copy(io.Discard, res.Body)
	if err != nil || n+1024 != size {
		t.Fatalf("read %d of %d: %v", n+1024, int64(size), err)
	}
}

// coverEPUB — EPUB с названием title и серой обложкой; при gray < 0 обложки нет.
func coverEPUB(t *testing.T, title string, gray int) string {
	t.Helper()
	var manifest, meta string
	files := map[string][]byte{"META-INF/container.xml": []byte(
		`<container><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`)}
	if gray >= 0 {
		img := image.NewGray(image.Rect(0, 0, 30, 40))
		for i := range img.Pix {
			img.Pix[i] = uint8(gray)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, nil); err != nil {
			t.Fatal(err)
		}
		files["c.jpg"] = buf.Bytes()
		manifest, meta = `<item id="c" href="c.jpg" media-type="image/jpeg"/>`, `<meta name="cover" content="c"/>`
	}
	files["content.opf"] = []byte(`<package xmlns:dc="http://purl.org/dc/elements/1.1/"><metadata><dc:title>` + title +
		`</dc:title><dc:creator>A</dc:creator>` + meta + `</metadata><manifest>` + manifest + `</manifest></package>`)
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range files {
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
	return buf.String()
}

func TestLibraryCover(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	dir := libraryFixture(t, admin, map[string]string{
		"a.epub": coverEPUB(t, "One", -1),
		"b.epub": coverEPUB(t, "One", 128),
		"c.pdf":  "no cover",
	})
	user := e.panelUser("alice", accounts.RoleUser)

	r := user.do("GET", "/api/v1/library/books", nil)
	r.expect(t, "catalog", 200, "")
	items, _ := r.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("catalog %v", r.body)
	}
	covers := map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		covers[m["filename"].(string)] = m["cover"]
	}
	// Файл с меньшим id (a.epub) без обложки: у книги — обложка второго файла.
	cover, _ := covers["a.epub"].(string)
	if cover == "" || covers["c.pdf"] != nil {
		t.Fatalf("covers %v", covers)
	}

	id := libraryIDs(t, user)["a.epub"]
	r = user.do("GET", fmt.Sprintf("/api/v1/library/books/%d", id), nil)
	r.expect(t, "book", 200, "")
	if book, _ := r.body["book"].(map[string]any); book["cover"] != cover {
		t.Fatalf("book %v, want %s", r.body, cover)
	}

	path := "/api/v1/library/covers/" + cover
	got := user.raw("GET", path, nil)
	if got.status != 200 || got.header.Get("Content-Type") != "image/jpeg" ||
		got.header.Get("Cache-Control") != "private, max-age=31536000, immutable" || got.header.Get("Etag") == "" {
		t.Fatalf("cover: %d %v", got.status, got.header)
	}
	if _, err := jpeg.Decode(strings.NewReader(got.body)); err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	if nm := user.raw("GET", path, map[string]string{"If-None-Match": got.header.Get("Etag")}); nm.status != 304 {
		t.Errorf("If-None-Match: %d", nm.status)
	}
	if anon := e.browser().raw("GET", path, nil); anon.status != 401 {
		t.Errorf("anonymous: %d", anon.status)
	}
	for _, bad := range []string{"..%2Fksb.sqlite3", "%25zz", strings.Repeat("0", 64), "x"} {
		if nf := user.raw("GET", "/api/v1/library/covers/"+bad, nil); nf.status != 404 || !strings.Contains(nf.body, "not_found") {
			t.Errorf("%s: %d %s", bad, nf.status, nf.body)
		}
	}

	partial, err := hashing.PartialMD5(filepath.Join(dir, "b.epub"))
	if err != nil {
		t.Fatal(err)
	}
	const unknown = "0123456789abcdef0123456789abcdef"
	reader := e.reader("alice", "pb")
	reader.sync(partial, 0.4, t0, "")
	reader.sync(unknown, 0.1, t0.Add(time.Second), "")
	r = user.do("GET", "/api/v1/books", nil)
	r.expect(t, "books", 200, "")
	progress := map[string]any{}
	books, _ := r.body["items"].([]any)
	for _, it := range books {
		m := it.(map[string]any)
		progress[m["hash"].(string)] = m["cover"]
	}
	if progress[partial] != cover || progress[unknown] != nil {
		t.Fatalf("progress covers %v, want %s", progress, cover)
	}
}
