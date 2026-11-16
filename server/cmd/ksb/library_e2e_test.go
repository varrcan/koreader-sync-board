package main

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
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
	waitStopped(stop, done, 100*time.Millisecond, log, "scanner")
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
	waitStopped(func() {}, ok, time.Second, slog.New(slog.NewJSONHandler(quiet, nil)), "scanner")
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
	epub := epubWithCover(t, "../../internal/library/meta/testdata/epub2.epub")
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

	cover, _ := b["cover"].(string)
	if cover == "" {
		t.Fatalf("no cover in book %v", b)
	}
	_, body = admin.call("GET", "/api/v1/library/books", nil)
	items, _ = body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["cover"] != cover {
		t.Fatalf("catalog %v, want cover %s", body, cover)
	}
	req, _ := http.NewRequest("GET", base+"/api/v1/library/covers/"+cover, nil)
	res, err := admin.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("cover: %d %v", res.StatusCode, res.Header)
	}
	if _, err := jpeg.Decode(res.Body); err != nil {
		t.Fatalf("cover is not a JPEG: %v", err)
	}
}

// epubWithCover переписывает архив, добавляя в манифест OPF обложку cover-image.
func epubWithCover(t *testing.T, src string) []byte {
	t.Helper()
	z, err := zip.OpenReader(src)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var img bytes.Buffer
	if err := jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 30, 40)), nil); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		e, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Write(data); err != nil {
			t.Fatal(err)
		}
	}
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
		if strings.HasSuffix(f.Name, ".opf") {
			item := `<item id="cover-img" href="cover.jpg" media-type="image/jpeg" properties="cover-image"/>`
			if bytes.Contains(data, []byte("</manifest>")) {
				data = bytes.Replace(data, []byte("</manifest>"), []byte(item+"</manifest>"), 1)
			} else {
				data = bytes.Replace(data, []byte("</package>"), []byte("<manifest>"+item+"</manifest></package>"), 1)
			}
			add(path.Join(path.Dir(f.Name), "cover.jpg"), img.Bytes())
		}
		add(f.Name, data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
