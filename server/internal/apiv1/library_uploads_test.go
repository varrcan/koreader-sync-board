package apiv1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
)

func (b *browser) uploadBook(name string, body []byte) resp {
	b.e.t.Helper()
	req, _ := http.NewRequest("POST", b.e.url+"/api/v1/library/uploads?name="+url.QueryEscape(name), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer res.Body.Close()
	r := resp{status: res.StatusCode, header: res.Header}
	_ = json.NewDecoder(res.Body).Decode(&r.body)
	return r
}

// uploaded — имена в каталоге загрузок, включая скрытые.
func (e *env) uploaded() []string {
	e.t.Helper()
	es, err := os.ReadDir(filepath.Join(e.dataDir, "uploads"))
	if err != nil {
		e.t.Fatal(err)
	}
	out := []string{}
	for _, f := range es {
		out = append(out, f.Name())
	}
	return out
}

func TestLibraryUpload(t *testing.T) {
	e := newEnv(t)
	user := e.panelUser("alice", accounts.RoleUser)

	r := user.uploadBook("Пикник.epub", []byte("epub content"))
	r.expect(t, "upload", 201, "")
	if r.body["name"] != "Пикник.epub" {
		t.Fatalf("body %v", r.body)
	}
	if got := e.uploaded(); !slices.Equal(got, []string{"Пикник.epub"}) {
		t.Fatalf("uploads %v", got)
	}
	if !strings.Contains(e.logs.String(), `"msg":"panel: book uploaded","user":"alice","name":"Пикник.epub","size":12`) {
		t.Fatalf("log %s", e.logs.String())
	}
	sum := waitScan(t, user)
	if sum.body["files"] != 1.0 || sum.body["roots"] != 0.0 {
		t.Fatalf("summary %v", sum.body)
	}
	book := libraryIDs(t, user)["Пикник.epub"]
	files := bookFiles(t, user, book)
	if len(files) != 1 || files[0]["uploaded"] != true || files[0]["converted"] != false || files[0]["path"] != "Пикник.epub" {
		t.Fatalf("files %v", files)
	}
}

func TestLibraryUploadRejected(t *testing.T) {
	e := newEnv(t)
	user := e.panelUser("alice", accounts.RoleUser)
	for _, c := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"notes.txt", "text", 415, "unsupported_format"},
		{"../a.epub", "x", 400, "invalid_name"},
		{".hidden.epub", "x", 400, "invalid_name"},
		{"a\nb.epub", "x", 400, "invalid_name"},
		{"", "x", 400, "invalid_name"},
		{"empty.epub", "", 400, "empty_file"},
	} {
		user.uploadBook(c.name, []byte(c.body)).expect(t, c.name, c.status, c.code)
	}
	if got := e.uploaded(); len(got) != 0 {
		t.Fatalf("uploads %v", got)
	}
	e.browser().uploadBook("a.epub", []byte("x")).expect(t, "anon", 401, "unauthorized")

	// Content-Length больше предела: отказ до чтения тела.
	req := httptest.NewRequest("POST", "/api/v1/library/uploads?name=big.epub", strings.NewReader("x"))
	req.ContentLength = 2 << 30
	base, _ := url.Parse(e.url)
	for _, c := range user.c.Jar.Cookies(base) {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	NewHandler(e.srv).ServeHTTP(w, req)
	if w.Code != 413 || !strings.Contains(w.Body.String(), "file_too_large") {
		t.Fatalf("content-length: %d %s", w.Code, w.Body)
	}
	if got := e.uploaded(); len(got) != 0 {
		t.Fatalf("uploads %v", got)
	}
}

func TestLibraryUploadDuplicate(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"Пикник.epub": "piknik"})
	book := libraryIDs(t, admin)["Пикник.epub"]
	user := e.panelUser("alice", accounts.RoleUser)

	r := user.uploadBook("picnic.epub", []byte("piknik"))
	r.expect(t, "duplicate", 409, "file_exists")
	if r.body["book_id"] != float64(book) {
		t.Fatalf("body %v, book %d", r.body, book)
	}
	if got := e.uploaded(); len(got) != 0 {
		t.Fatalf("uploads %v", got)
	}
	// То же имя с другим содержимым — не дубликат.
	user.uploadBook("Пикник.epub", []byte("other")).expect(t, "same name", 201, "")
}

func TestLibraryUploadNameTaken(t *testing.T) {
	e := newEnv(t)
	user := e.panelUser("alice", accounts.RoleUser)
	user.uploadBook("Пикник.fb2.zip", []byte("first")).expect(t, "first", 201, "")
	r := user.uploadBook("Пикник.fb2.zip", []byte("second"))
	r.expect(t, "second", 201, "")
	if r.body["name"] != "Пикник (2).fb2.zip" {
		t.Fatalf("body %v", r.body)
	}
	data, err := os.ReadFile(filepath.Join(e.dataDir, "uploads", "Пикник.fb2.zip"))
	if err != nil || string(data) != "first" {
		t.Fatalf("first: %q, %v", data, err)
	}
	// Имя в NFD приводится к NFC и совпадает с занятым.
	r = user.uploadBook("Й.epub", []byte("nfd"))
	r.expect(t, "nfd", 201, "")
	if r.body["name"] != "Й.epub" {
		t.Fatalf("nfd %v", r.body)
	}
}

// Обрыв посреди передачи не оставляет ни итогового, ни временного файла.
func TestLibraryUploadAborted(t *testing.T) {
	e := newEnv(t)
	user := e.panelUser("alice", accounts.RoleUser)
	closed := make(chan struct{}, 1)
	srv := httptest.NewUnstartedServer(NewHandler(e.srv))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateClosed {
			closed <- struct{}{}
		}
	}
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(e.url)
	var cookies []string
	for _, c := range user.c.Jar.Cookies(base) {
		cookies = append(cookies, c.String())
	}
	fmt.Fprintf(conn, "POST /api/v1/library/uploads?name=a.epub HTTP/1.1\r\nHost: x\r\nCookie: %s\r\n"+
		"Content-Type: application/octet-stream\r\nContent-Length: 1000000\r\n\r\n", strings.Join(cookies, "; "))
	if _, err := conn.Write(bytes.Repeat([]byte("x"), 64<<10)); err != nil {
		t.Fatal(err)
	}
	// Временный файл появился до обрыва.
	deadline := time.Now().Add(5 * time.Second)
	for len(e.uploaded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("upload did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	conn.Close()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not close the connection")
	}
	if got := e.uploaded(); len(got) != 0 {
		t.Fatalf("uploads %v", got)
	}
}

func TestLibraryDeleteUploaded(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	libraryFixture(t, admin, map[string]string{"regular.epub": "regular"})
	user := e.panelUser("alice", accounts.RoleUser)
	user.uploadBook("Пикник.epub", []byte("piknik")).expect(t, "upload", 201, "")
	user.uploadBook("gone.epub", []byte("gone")).expect(t, "upload", 201, "")
	waitScan(t, user)
	ids := libraryIDs(t, user)
	file := func(name string) int64 { return fileIDs(t, user, ids[name])[0] }
	del := func(id int64) resp { return user.do("DELETE", fmt.Sprintf("/api/v1/library/files/%d", id), nil) }

	del(file("regular.epub")).expect(t, "regular by user", 409, "file_read_only")
	admin.do("DELETE", fmt.Sprintf("/api/v1/library/files/%d", file("regular.epub")), nil).
		expect(t, "regular by admin", 409, "file_read_only")

	del(file("Пикник.epub")).expect(t, "uploaded", 204, "")
	if got := e.uploaded(); !slices.Equal(got, []string{"gone.epub"}) {
		t.Fatalf("uploads %v", got)
	}
	if !strings.Contains(e.logs.String(), `"msg":"panel: uploaded file deleted","user":"alice","path":"Пикник.epub"`) {
		t.Fatalf("log %s", e.logs.String())
	}
	user.do("GET", fmt.Sprintf("/api/v1/library/books/%d", ids["Пикник.epub"]), nil).expect(t, "book gone", 404, "not_found")

	// Файл уже исчез с диска: из индекса он всё равно уходит.
	if err := os.Remove(filepath.Join(e.dataDir, "uploads", "gone.epub")); err != nil {
		t.Fatal(err)
	}
	del(file("gone.epub")).expect(t, "vanished", 204, "")
	waitScan(t, user)
	if got := libraryIDs(t, user); len(got) != 1 {
		t.Fatalf("catalog %v", got)
	}
	del(9999).expect(t, "missing", 404, "not_found")
}

// Удаление и загрузка ставят скан загрузок, а не полный: обычный корень не обходится.
func TestLibraryUploadScansOnlyUploads(t *testing.T) {
	e := newEnv(t)
	admin := e.panelUser("root", accounts.RoleAdmin)
	dir := libraryFixture(t, admin, map[string]string{"a.epub": "a"})
	// Новый файл обычного корня появится только после полного скана.
	if err := os.WriteFile(filepath.Join(dir, "b.epub"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	admin.uploadBook("c.epub", []byte("c")).expect(t, "upload", 201, "")
	waitScan(t, admin)
	got := libraryIDs(t, admin)
	if _, ok := got["b.epub"]; ok || len(got) != 2 {
		t.Fatalf("catalog %v", got)
	}
}
