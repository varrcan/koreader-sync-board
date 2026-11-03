package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/apiv1"
	"github.com/varrcan/koreader-sync-board/server/internal/buildinfo"
	"github.com/varrcan/koreader-sync-board/server/internal/web"
)

func tag(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, name) })
}

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	// Регистрация пересекающихся шаблонов паникует — New не должен.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route registration panicked: %v", r)
		}
	}()
	return New(Handlers{Kosync: tag("kosync"), API: apiv1.NewHandler(), Web: web.Handler()})
}

func TestRouting(t *testing.T) {
	h := newTestServer(t)
	tests := []struct {
		method, path string
		status       int
		contains     string
		ctype        string
	}{
		{"GET", "/users/auth", 200, "kosync", ""},
		{"POST", "/users/create", 200, "kosync", ""},
		{"PUT", "/syncs/progress", 200, "kosync", ""},
		{"GET", "/syncs/progress/abc", 200, "kosync", ""},
		{"GET", "/syncs/progress/ab-c", 200, "kosync", ""},
		{"GET", "/healthcheck", 200, "kosync", ""},
		{"GET", "/healthcheck/", 200, "kosync", ""},
		{"GET", "/api/v1/health", 200, `"status":"ok"`, "application/json"},
		{"GET", "/api/v1/version", 200, `"version"`, "application/json"},
		{"GET", "/api/v1/unknown", 404, `"code":"not_found"`, "application/json"},
		{"POST", "/api/v1/health", 404, `"code":"not_found"`, "application/json"},
		{"GET", "/api/", 404, `"code":"not_found"`, "application/json"},
		{"GET", "/", 200, "<html", "text/html; charset=utf-8"},
		{"GET", "/library", 200, "<html", "text/html; charset=utf-8"},
		{"POST", "/library", 405, "", ""},
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
		if tt.ctype != "" && w.Header().Get("Content-Type") != tt.ctype {
			t.Errorf("%s %s: Content-Type %q", tt.method, tt.path, w.Header().Get("Content-Type"))
		}
	}
}

func TestVersionFromLdflags(t *testing.T) {
	old, oldCommit := buildinfo.Version, buildinfo.Commit
	buildinfo.Version, buildinfo.Commit = "v9.9.9", "deadbeef"
	defer func() { buildinfo.Version, buildinfo.Commit = old, oldCommit }()
	w := httptest.NewRecorder()
	newTestServer(t).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/version", nil))
	var v apiv1.Version
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != "v9.9.9" || v.Commit != "deadbeef" {
		t.Fatalf("version = %+v", v)
	}
}
