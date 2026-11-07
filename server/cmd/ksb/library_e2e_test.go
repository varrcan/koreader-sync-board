package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

// TestWaitHungScanner: сканер, зависший в обходе (как на hard-mount NFS), не
// держит остановку дольше предела, а в логе остаётся предупреждение.
func TestWaitHungScanner(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hang := make(chan struct{})
	entered := make(chan struct{})
	defer close(hang)
	walk := func(string, fs.WalkDirFunc) error {
		close(entered)
		<-hang // отмена контекста системный вызов не прерывает
		return nil
	}
	lib := library.NewStore(db)
	if _, err := lib.AddRoot(ctx, t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	sc := library.NewScanner(lib, 0, log, library.WithWalk(walk))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { sc.Run(runCtx); close(done) }()
	sc.Trigger()
	<-entered

	start := time.Now()
	waitScanner(stop, done, 100*time.Millisecond, log)
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Fatalf("waited %v", d)
	}
	if !strings.Contains(logs.String(), `"msg":"library: scanner did not stop"`) {
		t.Fatalf("no warning:\n%s", logs)
	}

	// Сканер, который останавливается вовремя, ждётся без предупреждения.
	quiet := &syncBuffer{}
	ok := make(chan struct{})
	close(ok)
	waitScanner(func() {}, ok, time.Second, slog.New(slog.NewJSONHandler(quiet, nil)))
	if quiet.String() != "" {
		t.Fatalf("unexpected log: %s", quiet)
	}
}

// TestLibraryE2E: администратор добавляет корень с EPUB, сервер его сканирует, а
// читалка синхронизирует partial MD5 этого EPUB без metadata — в «Книгах»
// название из EPUB.
func TestLibraryE2E(t *testing.T) {
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "admin-password")
	t.Setenv("KSB_LIBRARY_SCAN_INTERVAL", "0")
	books := t.TempDir()
	epub, err := os.ReadFile("../../internal/library/meta/testdata/epub2.epub")
	if err != nil {
		t.Fatal(err)
	}
	book := filepath.Join(books, "fantasy", "Piknik.epub")
	if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(book, epub, 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := hashing.PartialMD5(book)
	if err != nil {
		t.Fatal(err)
	}
	base, logs := startServer(t)

	admin := newPanel(t, base, "198.51.100.50")
	if code, _ := admin.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password"}); code != 200 {
		t.Fatalf("login: %d", code)
	}
	if code, body := admin.call("POST", "/api/v1/library/roots", map[string]string{"path": books}); code != 201 {
		t.Fatalf("add root: %d %v", code, body)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		_, body := admin.call("GET", "/api/v1/library", nil)
		if scan, _ := body["scan"].(map[string]any); scan["running"] == false && body["files"] == 1.0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan did not finish:\n%s", logs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), `"msg":"library: scan finished"`) {
		t.Fatalf("no scan log:\n%s", logs)
	}

	code, body := admin.call("POST", "/api/v1/devices", map[string]string{"name": "Kobo"})
	if code != 201 {
		t.Fatalf("create device: %d %v", code, body)
	}
	kobo := reader{t: t, base: base, ip: "198.51.100.51", user: "admin", password: body["password"].(string)}
	kobo.push(hash, 0.25).expect(t, "push", 200, "")

	code, body = admin.call("GET", "/api/v1/books", nil)
	items, _ := body["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatalf("books: %d %v", code, body)
	}
	b := items[0].(map[string]any)
	if b["title"] != "Пикник на обочине." || b["authors"] != "Стругацкий Аркадий, Стругацкий Борис" || b["filename"] != "Piknik.epub" {
		t.Fatalf("book %v", b)
	}
	if counts, _ := body["counts"].(map[string]any); counts["unknown"] != 0.0 {
		t.Fatalf("counts %v", body["counts"])
	}
}
