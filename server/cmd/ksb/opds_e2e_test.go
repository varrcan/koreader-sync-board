package main

import (
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
)

type opdsFeed struct {
	Links []opdsLink `xml:"link"`
	Entry []struct {
		Title string     `xml:"title"`
		Links []opdsLink `xml:"link"`
	} `xml:"entry"`
}

type opdsLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr"`
}

// TestOPDSE2E: читалка с паролем устройства открывает каталог настоящего serve, ищет
// книгу и скачивает файл с тем же partial MD5, что у файла библиотеки.
func TestOPDSE2E(t *testing.T) {
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "admin-password")
	t.Setenv("KSB_LIBRARY_SCAN_INTERVAL", "0")
	books := t.TempDir()
	source := filepath.Join(books, "Пикник.epub")
	epub, err := os.ReadFile("../../internal/library/meta/testdata/epub2.epub")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, epub, 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := hashing.PartialMD5(source)
	if err != nil {
		t.Fatal(err)
	}
	base, logs := startServer(t)

	admin := newPanel(t, base, "198.51.100.60")
	if code, _ := admin.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password"}); code != 200 {
		t.Fatalf("login: %d", code)
	}
	if code, body := admin.call("POST", "/api/v1/library/roots", map[string]string{"path": books}); code != 201 {
		t.Fatalf("add root: %d %v", code, body)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		_, body := admin.call("GET", "/api/v1/library", nil)
		if scan, _ := body["scan"].(map[string]any); scan["running"] == false && body["files"] == 1.0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan did not finish:\n%s", logs)
		}
	}
	code, body := admin.call("POST", "/api/v1/devices", map[string]string{"name": "PocketBook"})
	if code != 201 {
		t.Fatalf("create device: %d %v", code, body)
	}
	password := body["password"].(string)

	get := func(target string) (int, []byte, http.Header) {
		t.Helper()
		req, err := http.NewRequest("GET", base+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("admin", password)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, data, res.Header
	}
	feedAt := func(target string) opdsFeed {
		t.Helper()
		code, data, _ := get(target)
		if code != 200 {
			t.Fatalf("%s: %d %s", target, code, data)
		}
		var f opdsFeed
		if err := xml.Unmarshal(data, &f); err != nil {
			t.Fatalf("%s: %v\n%s", target, err, data)
		}
		return f
	}

	res, err := http.Get(base + "/opds")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("without password: %d", res.StatusCode)
	}

	root := feedAt("/opds")
	if len(root.Entry) != 1 || root.Entry[0].Links[0].Href != "/opds/books" {
		t.Fatalf("root %+v", root)
	}
	all := feedAt(root.Entry[0].Links[0].Href)
	if len(all.Entry) != 1 || all.Entry[0].Title != "Пикник на обочине." {
		t.Fatalf("books %+v", all)
	}
	found := feedAt("/opds/search?q=%D0%BF%D0%B8%D0%BA%D0%BD%D0%B8%D0%BA")
	if len(found.Entry) != 1 {
		t.Fatalf("search %+v", found)
	}

	href := ""
	for _, l := range found.Entry[0].Links {
		if strings.HasPrefix(l.Rel, "http://opds-spec.org/acquisition") {
			href = l.Href
		}
	}
	code, data, header := get(href)
	if code != 200 || header.Get("Content-Type") != "application/epub+zip" {
		t.Fatalf("download %s: %d %v", href, code, header)
	}
	saved := filepath.Join(t.TempDir(), "saved.epub")
	if err := os.WriteFile(saved, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := hashing.PartialMD5(saved); err != nil || got != want {
		t.Fatalf("partial MD5 %s, want %s, %v", got, want, err)
	}
}
