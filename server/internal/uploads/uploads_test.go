package uploads

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"Пикник.epub", "Пикник.epub", true},
		{"Йо̆.epub", "Йо̆.epub", true},
		{"Й.epub", "Й.epub", true}, // NFD → NFC
		{"a b (1).fb2.zip", "a b (1).fb2.zip", true},
		{"", "", false},
		{".hidden.epub", "", false},
		{"..", "", false},
		{"../a.epub", "", false},
		{"a/b.epub", "", false},
		{`a\b.epub`, "", false},
		{"a\x00.epub", "", false},
		{"a\n.epub", "", false},
		{"a\x7f.epub", "", false},
		{"\xff.epub", "", false},
		{strings.Repeat("я", 125) + ".epub", strings.Repeat("я", 125) + ".epub", true}, // 255 байт
		{strings.Repeat("я", 126) + ".epub", "", false},
	} {
		got, err := ValidName(tc.in)
		if tc.ok != (err == nil) || got != tc.want {
			t.Errorf("%q: %q, %v", tc.in, got, err)
		}
	}
}

func TestCandidates(t *testing.T) {
	first := func(name, format string, n int) []string {
		var out []string
		for c := range candidates(name, format) {
			out = append(out, c)
			if len(out) == n {
				break
			}
		}
		return out
	}
	for _, tc := range []struct {
		name, format string
		want         []string
	}{
		{"Пикник.fb2.zip", "fb2.zip", []string{"Пикник.fb2.zip", "Пикник (2).fb2.zip", "Пикник (3).fb2.zip"}},
		{"A.EPUB", "epub", []string{"A.EPUB", "A (2).EPUB", "A (3).EPUB"}},
		{"a.b.Fb2.ZIP", "fb2.zip", []string{"a.b.Fb2.ZIP", "a.b (2).Fb2.ZIP", "a.b (3).Fb2.ZIP"}},
	} {
		if got := first(tc.name, tc.format, 3); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
	long := strings.Repeat("я", 125) + ".epub"
	got := first(long, "epub", 2)[1]
	if len(got) > maxName || !strings.HasSuffix(got, "я (2).epub") {
		t.Errorf("long: %d bytes, %q", len(got), got)
	}
	n := 0
	for range candidates("a.epub", "epub") {
		n++
	}
	if n != maxCandidates {
		t.Errorf("%d candidates", n)
	}
}

func openDir(t *testing.T) (*Dir, string) {
	t.Helper()
	path := t.TempDir()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

// files — имена в каталоге, включая скрытые.
func files(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func receive(t *testing.T, d *Dir, dir, content string) string {
	t.Helper()
	tmp, size, err := d.Receive(dir, strings.NewReader(content), MaxSize)
	if err != nil || size != int64(len(content)) {
		t.Fatalf("receive: %d, %v", size, err)
	}
	if !strings.HasPrefix(filepath.Base(tmp), ".ksb-upload-") {
		t.Fatalf("temp %q", tmp)
	}
	return tmp
}

func TestPlace(t *testing.T) {
	d, path := openDir(t)
	name, err := d.Place(receive(t, d, ".", "first"), "Пикник.fb2.zip", "fb2.zip")
	if err != nil || name != "Пикник.fb2.zip" {
		t.Fatalf("%q, %v", name, err)
	}
	name, err = d.Place(receive(t, d, ".", "second"), "Пикник.fb2.zip", "fb2.zip")
	if err != nil || name != "Пикник (2).fb2.zip" {
		t.Fatalf("%q, %v", name, err)
	}
	if got := files(t, path); !slices.Equal(got, []string{"Пикник (2).fb2.zip", "Пикник.fb2.zip"}) {
		t.Fatalf("files %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(path, "Пикник.fb2.zip")); string(b) != "first" {
		t.Fatalf("first overwritten: %q", b)
	}
}

func TestPlaceRace(t *testing.T) {
	d, path := openDir(t)
	const n = 8
	tmps := make([]string, n)
	for i := range tmps {
		tmps[i] = receive(t, d, ".", fmt.Sprint(i))
	}
	var wg sync.WaitGroup
	names := make([]string, n)
	for i := range tmps {
		wg.Go(func() {
			name, err := d.Place(tmps[i], "a.epub", "epub")
			if err != nil {
				t.Error(err)
			}
			names[i] = name
		})
	}
	wg.Wait()
	slices.Sort(names)
	if len(slices.Compact(names)) != n {
		t.Fatalf("names %v", names)
	}
	if got := files(t, path); len(got) != n {
		t.Fatalf("files %v", got)
	}
}

type brokenReader struct{ n int }

func (r *brokenReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := min(len(p), r.n)
	r.n -= n
	return n, nil
}

func TestReceiveBroken(t *testing.T) {
	d, path := openDir(t)
	_, _, err := d.Receive(".", &brokenReader{n: 100 << 10}, MaxSize)
	var re ReadError
	if !errors.As(err, &re) {
		t.Fatalf("%v", err)
	}
	if got := files(t, path); len(got) != 0 {
		t.Fatalf("files %v", got)
	}
}

// endless — бесконечное тело без Content-Length.
type endless struct{}

func (endless) Read(p []byte) (int, error) { return len(p), nil }

func TestReceiveTooLarge(t *testing.T) {
	d, path := openDir(t)
	if _, _, err := d.Receive(".", endless{}, 1<<20); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("%v", err)
	}
	if got := files(t, path); len(got) != 0 {
		t.Fatalf("files %v", got)
	}
	// Ровно предел — можно.
	if _, size, err := d.Receive(".", bytes.NewReader(make([]byte, 1<<20)), 1<<20); err != nil || size != 1<<20 {
		t.Fatalf("at limit: %d, %v", size, err)
	}
}

func TestReplace(t *testing.T) {
	d, path := openDir(t)
	if err := os.Mkdir(filepath.Join(path, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"old", "new"} {
		if err := d.Replace(receive(t, d, "sub", content), "sub/a.epub"); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(path, "sub", "a.epub")); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
	if got := files(t, filepath.Join(path, "sub")); !slices.Equal(got, []string{"a.epub"}) {
		t.Fatalf("files %v", got)
	}
	if _, _, err := d.Receive("missing", strings.NewReader("x"), MaxSize); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir: %v", err)
	}
	if err := d.Remove("sub/a.epub"); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("sub/a.epub"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestEscape(t *testing.T) {
	d, path := openDir(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "victim.epub"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"..", "link", "../" + filepath.Base(outside)} {
		if _, _, err := d.Receive(dir, strings.NewReader("x"), MaxSize); err == nil {
			t.Errorf("receive into %q succeeded", dir)
		}
	}
	tmp := receive(t, d, ".", "x")
	if err := d.Replace(tmp, "link/victim.epub"); err == nil {
		t.Error("replace through symlink succeeded")
	}
	if err := d.Remove("link/victim.epub"); err == nil {
		t.Error("remove through symlink succeeded")
	}
	if err := d.Remove("../" + filepath.Base(outside) + "/victim.epub"); err == nil {
		t.Error("remove through .. succeeded")
	}
	if got := files(t, outside); !slices.Equal(got, []string{"victim.epub"}) {
		t.Fatalf("outside %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "victim.epub")); string(b) != "v" {
		t.Fatalf("victim %q", b)
	}
}

func TestCleanTemp(t *testing.T) {
	d, path := openDir(t)
	if err := os.Mkdir(filepath.Join(path, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := receive(t, d, "sub", "old")
	fresh := receive(t, d, ".", "fresh")
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(path, old), stale, stale); err != nil {
		t.Fatal(err)
	}
	// Старая книга с похожим именем, но без префикса, остаётся.
	if err := os.WriteFile(filepath.Join(path, "ksb-upload-book.epub"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(path, "ksb-upload-book.epub"), stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := d.CleanTemp(); err != nil {
		t.Fatal(err)
	}
	if got := files(t, filepath.Join(path, "sub")); len(got) != 0 {
		t.Fatalf("sub %v", got)
	}
	if got := files(t, path); !slices.Equal(got, []string{fresh, "ksb-upload-book.epub", "sub"}) {
		t.Fatalf("root %v", got)
	}
}

// Медленный, но живой клиент передаёт дольше ReadTimeout сервера и не обрывается.
func TestDeadlineReader(t *testing.T) {
	for _, extend := range []bool{true, false} {
		t.Run(fmt.Sprint("extend=", extend), func(t *testing.T) {
			got := make(chan error, 1)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body io.Reader = r.Body
				if extend {
					body = newDeadlineReader(w, r.Body, 300*time.Millisecond, 10*time.Millisecond)
				}
				n, err := io.Copy(io.Discard, body)
				if err == nil && n != 20 {
					err = fmt.Errorf("read %d bytes", n)
				}
				got <- err
			}))
			srv.Config.ReadTimeout = 200 * time.Millisecond
			srv.Start()
			defer srv.Close()

			pr, pw := io.Pipe()
			go func() {
				for range 20 {
					time.Sleep(50 * time.Millisecond)
					if _, err := pw.Write([]byte{'x'}); err != nil {
						return
					}
				}
				pw.Close()
			}()
			req, err := http.NewRequest(http.MethodPost, srv.URL, pr)
			if err != nil {
				t.Fatal(err)
			}
			if resp, err := srv.Client().Do(req); err == nil {
				resp.Body.Close()
			}
			pr.Close()
			err = <-got
			if extend && err != nil {
				t.Fatalf("slow client dropped: %v", err)
			}
			if !extend && err == nil {
				t.Fatal("control: server ReadTimeout did not fire")
			}
		})
	}
}
