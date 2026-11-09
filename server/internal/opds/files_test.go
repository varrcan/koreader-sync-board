package opds

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing"
	"github.com/varrcan/koreader-sync-board/server/internal/library/convert"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

func hrefOf(t *testing.T, f feed, format string) string {
	t.Helper()
	for _, entry := range f.Entries {
		for _, l := range entry.Links {
			if l.Rel == acquisitionRel && l.Title == strings.ToUpper(format) {
				return l.Href
			}
		}
	}
	t.Fatalf("no %s link in %+v", format, f.Entries)
	return ""
}

func TestDownload(t *testing.T) {
	e := newEnv(t)
	content := strings.Repeat("книга ", 1000)
	e.scan(map[string]string{"sub/a.epub": content})
	href := hrefOf(t, e.feedAt("/opds/books"), "epub")

	r := e.get(href)
	r.expect(t, "download", 200)
	if string(r.body) != content {
		t.Fatalf("body differs: %d bytes", len(r.body))
	}
	if r.header.Get("Content-Type") != "application/epub+zip" {
		t.Fatalf("headers %v", r.header)
	}

	var want string
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT partial_md5 FROM library_files`).Scan(&want); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(t.TempDir(), "saved.epub")
	if err := os.WriteFile(saved, r.body, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := hashing.PartialMD5(saved); err != nil || got != want {
		t.Fatalf("partial MD5 %s, want %s, %v", got, want, err)
	}

	head := e.request("HEAD", href, e.pass, nil)
	head.expect(t, "head", 200)
	if len(head.body) != 0 || head.header.Get("Content-Length") != fmt.Sprint(len(content)) {
		t.Fatalf("head %v", head.header)
	}

	part := e.request("GET", href, e.pass, map[string]string{"Range": "bytes=2-5"})
	part.expect(t, "range", http.StatusPartialContent)
	if string(part.body) != content[2:6] {
		t.Fatalf("range %q", part.body)
	}
}

func TestDownloadUnavailable(t *testing.T) {
	e := newEnv(t)
	e.scan(map[string]string{"a.epub": "a"})
	href := hrefOf(t, e.feedAt("/opds/books"), "epub")
	if err := os.Remove(filepath.Join(e.root, "a.epub")); err != nil {
		t.Fatal(err)
	}
	e.get(href).expect(t, "gone", 404)
	e.get("/opds/files/9999").expect(t, "unknown", 404)
	e.get("/opds/files/abc").expect(t, "bad id", 404)
}

// koreaderFilename повторяет разбор заголовка в OPDSBrowser:getServerFileName.
func koreaderFilename(t *testing.T, disposition string) string {
	t.Helper()
	m := regexp.MustCompile(`filename="([^"]+)"`).FindStringSubmatch(disposition)
	if m == nil {
		t.Fatalf("no quoted filename in %q", disposition)
	}
	name, err := url.PathUnescape(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestContentDisposition(t *testing.T) {
	e := newEnv(t)
	e.scan(map[string]string{"Война и мир.epub": "a"})
	r := e.request("HEAD", hrefOf(t, e.feedAt("/opds/books"), "epub"), e.pass, nil)
	r.expect(t, "head", 200)
	d := r.header.Get("Content-Disposition")
	if got := koreaderFilename(t, d); got != "Война и мир.epub" {
		t.Fatalf("KOReader name %q from %q", got, d)
	}
	if !strings.Contains(d, "filename*=") {
		t.Fatalf("no filename* in %q", d)
	}
	if _, params, err := mime.ParseMediaType(d); err != nil || params["filename"] != "Война и мир.epub" {
		t.Fatalf("RFC 6266 client: %v, %v", params, err)
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// countingConverter кладёт готовый EPUB в файл результата после pause и дописывает
// строку в счётчик запусков.
func countingConverter(t *testing.T, pause string) (exe, counter string) {
	t.Helper()
	epub, err := filepath.Abs(testEPUB)
	if err != nil {
		t.Fatal(err)
	}
	counter = filepath.Join(t.TempDir(), "runs")
	return script(t, fmt.Sprintf("echo run >> %q\nsleep %s\ncp %q \"$5\"", counter, pause, epub)), counter
}

func runs(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func onDemandLinks(f feed) []link {
	out := []link{}
	for _, entry := range f.Entries {
		for _, l := range entry.Links {
			if strings.HasSuffix(l.Href, "/epub") {
				out = append(out, l)
			}
		}
	}
	return out
}

func TestOnDemandLink(t *testing.T) {
	e := newEnv(t)
	e.books["a.fb2"] = meta.Book{Title: "A", Authors: []string{"X"}}
	e.books["a.fb2.zip"] = meta.Book{Title: "A", Authors: []string{"X"}}
	e.scan(map[string]string{"a.fb2": "1"})
	e.scan(map[string]string{"a.fb2.zip": "2"})
	links := onDemandLinks(e.feedAt("/opds/books"))
	if len(links) != 1 || links[0].Type != "application/epub+zip" || links[0].Rel != acquisitionRel || links[0].Length != 0 {
		t.Fatalf("links %+v", links)
	}
	var first int64
	if err := e.db.Read.QueryRowContext(e.ctx, `SELECT min(id) FROM library_files`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("/opds/files/%d/epub", first); links[0].Href != want {
		t.Fatalf("href %s, want %s", links[0].Href, want)
	}

	e.books["a.epub"] = meta.Book{Title: "A", Authors: []string{"X"}}
	e.scan(map[string]string{"a.epub": "3"})
	if links := onDemandLinks(e.feedAt("/opds/books")); len(links) != 0 {
		t.Fatalf("EPUB exists: %+v", links)
	}
}

func TestOnDemandDownload(t *testing.T) {
	e := newEnv(t)
	exe, counter := countingConverter(t, "0")
	e.useConverter(exe)
	e.scan(map[string]string{"Пикник.fb2": "fb2"})
	f := e.feedAt("/opds/books")
	href := onDemandLinks(f)[0].Href

	want, err := os.ReadFile(testEPUB)
	if err != nil {
		t.Fatal(err)
	}
	first := e.get(href)
	first.expect(t, "first", 200)
	if string(first.body) != string(want) || first.header.Get("Content-Type") != "application/epub+zip" {
		t.Fatalf("first: %d bytes, %v", len(first.body), first.header)
	}
	if got := koreaderFilename(t, first.header.Get("Content-Disposition")); got != "Пикник.epub" {
		t.Fatalf("name %q", got)
	}
	second := e.get(href)
	second.expect(t, "second", 200)
	if string(second.body) != string(want) {
		t.Fatal("second download differs")
	}
	if n := runs(t, counter); n != 1 {
		t.Fatalf("%d conversions", n)
	}

	after := e.feedAt("/opds/books")
	if len(after.Entries) != 1 || len(onDemandLinks(after)) != 0 {
		t.Fatalf("after conversion: %+v", after.Entries)
	}
	direct := e.get(hrefOf(t, after, "epub"))
	direct.expect(t, "direct", 200)
	if string(direct.body) != string(want) {
		t.Fatal("result is not served as a regular file")
	}
}

func TestOnDemandHead(t *testing.T) {
	e := newEnv(t)
	exe, counter := countingConverter(t, "0")
	e.useConverter(exe)
	e.scan(map[string]string{"Война и мир.fb2": "fb2", "doc.pdf": "pdf"})
	f := e.feedAt("/opds/books")
	href := onDemandLinks(f)[0].Href
	r := e.request("HEAD", href, e.pass, nil)
	r.expect(t, "head", 200)
	if r.header.Get("Content-Type") != "application/epub+zip" || r.header.Get("Content-Length") != "" {
		t.Fatalf("headers %v", r.header)
	}
	if got := koreaderFilename(t, r.header.Get("Content-Disposition")); got != "Война и мир.epub" {
		t.Fatalf("name %q", got)
	}
	if n := runs(t, counter); n != 0 {
		t.Fatalf("HEAD started %d conversions", n)
	}
	pdf := hrefOf(t, f, "pdf") + "/epub"
	e.request("HEAD", pdf, e.pass, nil).expect(t, "HEAD pdf", 404)
	e.request("HEAD", "/opds/files/9999/epub", e.pass, nil).expect(t, "HEAD unknown", 404)
}

func TestOnDemandDisconnect(t *testing.T) {
	e := newEnv(t)
	exe, counter := countingConverter(t, "0.3")
	e.useConverter(exe)
	e.scan(map[string]string{"a.fb2": "fb2"})
	href := onDemandLinks(e.feedAt("/opds/books"))[0].Href

	ctx, cancel := context.WithTimeout(e.ctx, 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", e.url+href, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("alice", e.pass)
	if res, err := http.DefaultTransport.RoundTrip(req); err == nil {
		res.Body.Close()
		t.Fatalf("status %d before the conversion finished", res.StatusCode)
	}

	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if len(onDemandLinks(e.feedAt("/opds/books"))) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("conversion was cancelled")
		}
	}
	e.get(href).expect(t, "after disconnect", 200)
	if n := runs(t, counter); n != 1 {
		t.Fatalf("%d conversions", n)
	}
}

func TestOnDemandErrors(t *testing.T) {
	e := newEnv(t)
	e.books["big.fb2"] = meta.Book{Title: "big", Authors: []string{"big"}}
	e.books["broken.fb2"] = meta.Book{Title: "broken", Authors: []string{"broken"}}
	e.books["gone.fb2"] = meta.Book{Title: "gone", Authors: []string{"gone"}}
	e.books["slow.fb2"] = meta.Book{Title: "slow", Authors: []string{"slow"}}
	e.books["a.pdf"] = meta.Book{Title: "pdf", Authors: []string{"pdf"}}
	e.useConverter(script(t, "exit 1"))
	e.scan(map[string]string{"big.fb2": "big one", "broken.fb2": "bad", "gone.fb2": "gone", "slow.fb2": "slow one", "a.pdf": "pdf"})
	hrefs := map[string]string{}
	pdf := ""
	f := e.feedAt("/opds/books")
	for _, entry := range f.Entries {
		for _, l := range entry.Links {
			if strings.HasSuffix(l.Href, "/epub") {
				hrefs[entry.Title] = l.Href
			}
			if entry.Title == "pdf" {
				pdf = l.Href
			}
		}
	}

	e.get(pdf+"/epub").expect(t, "not fb2", 404)
	e.get("/opds/files/9999/epub").expect(t, "unknown", 404)
	r := e.get(hrefs["broken"])
	r.expect(t, "broken", 422)

	e.useConverter(script(t, "exit 4"))
	e.get(hrefs["gone"]).expect(t, "gone", 404)
	e.useConverter(script(t, "exit 3"))
	e.get(hrefs["big"]).expect(t, "big", 413)
	e.useConverter(script(t, "exec sleep 30"), convert.WithTimeout(100*time.Millisecond))
	e.get(hrefs["slow"]).expect(t, "slow", 504)
}

func TestOnDemandOutlivesServerTimeouts(t *testing.T) {
	e := newEnv(t)
	exe, _ := countingConverter(t, "0.5")
	e.useConverter(exe)
	e.scan(map[string]string{"a.fb2": "fb2"})
	slow := httptest.NewUnstartedServer(e.h)
	slow.Config.ReadTimeout = 100 * time.Millisecond
	slow.Config.WriteTimeout = 100 * time.Millisecond
	slow.Start()
	t.Cleanup(slow.Close)
	href := onDemandLinks(e.feedAt("/opds/books"))[0].Href
	e.url = slow.URL
	r := e.get(href)
	r.expect(t, "convert", 200)
	if len(r.body) == 0 {
		t.Fatal("empty response")
	}
}
