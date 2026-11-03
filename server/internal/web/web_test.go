package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPA(t *testing.T) {
	h := newHandler(fstest.MapFS{
		"index.html":       {Data: []byte("<html>index</html>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/nested/.x": {Data: []byte("")},
	})
	tests := []struct {
		method, path string
		status       int
		contains     string
		cache        string
	}{
		{"GET", "/", 200, "index", "no-cache"},
		{"GET", "/library", 200, "index", "no-cache"},
		{"GET", "/library/books/42", 200, "index", "no-cache"},
		{"GET", "/index.html", 200, "index", "no-cache"},
		{"GET", "/assets/app-1.js", 200, "console.log", "public, max-age=31536000, immutable"},
		{"GET", "/favicon.svg", 200, "<svg/>", ""},
		{"GET", "/assets/nested", 200, "index", "no-cache"},
		{"HEAD", "/library", 200, "", "no-cache"},
		{"POST", "/library", 405, "", ""},
		{"DELETE", "/", 405, "", ""},
	}
	for _, tt := range tests {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
		if w.Code != tt.status {
			t.Errorf("%s %s: status %d, want %d", tt.method, tt.path, w.Code, tt.status)
			continue
		}
		if !strings.Contains(w.Body.String(), tt.contains) {
			t.Errorf("%s %s: body %q", tt.method, tt.path, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != tt.cache {
			t.Errorf("%s %s: Cache-Control %q, want %q", tt.method, tt.path, got, tt.cache)
		}
		if tt.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s %s: Allow %q", tt.method, tt.path, w.Header().Get("Allow"))
		}
	}
}

// TestEmbeddedIndex проверяет, что сборка (с тегом или без) содержит index.html.
func TestEmbeddedIndex(t *testing.T) {
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
}
