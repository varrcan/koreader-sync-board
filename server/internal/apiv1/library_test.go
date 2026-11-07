package apiv1

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
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
	if n := e.count(`SELECT count(*) FROM library_roots`); n != 0 {
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
