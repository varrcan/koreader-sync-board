package lookup

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

// coverServer — TLS-сервер, к которому приводит любой хост: проверка хостов
// работает на настоящих адресах.
func coverServer(t *testing.T, h http.HandlerFunc) (*CoverFetcher, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	return newCoverFetcher(&http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // самоподписанный сервер теста
	}), &hits
}

func TestValidCoverURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://covers.openlibrary.org/b/id/1-L.jpg?default=false": true,
		"https://archive.org/download/x":                            true,
		"https://ia801507.us.archive.org/x.jpg":                     true,
		"https://fantlab.ru/images/editions/big/1":                  true,
		"https://books.google.com/books/content?id=1":               true,
		"https://lh3.googleusercontent.com/a":                       true,
		"http://covers.openlibrary.org/b/id/1-L.jpg":                false,
		"https://192.168.1.1/a.jpg":                                 false,
		"https://evilarchive.org/a":                                 false,
		"https://archive.org.evil.com/a":                            false,
		"https://user@fantlab.ru/a":                                 false,
		"https://fantlab.ru:8443/a":                                 false,
		"not a url":                                                 false,
	} {
		eq(t, raw, ValidCoverURL(raw), want)
	}
}

func TestFetchCoverRedirectChain(t *testing.T) {
	f, _ := coverServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "covers.openlibrary.org":
			http.Redirect(w, r, "https://archive.org/download/a", http.StatusFound)
		case "archive.org":
			http.Redirect(w, r, "https://ia801507.us.archive.org/a.jpg", http.StatusFound)
		default:
			_, _ = w.Write([]byte("image"))
		}
	})
	got, err := f.Fetch(ctx(), "https://covers.openlibrary.org/b/id/1-L.jpg?default=false")
	if err != nil || string(got) != "image" {
		t.Fatalf("%q, %v", got, err)
	}
}

func TestFetchCoverForbiddenURLNotRequested(t *testing.T) {
	f, hits := coverServer(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := f.Fetch(ctx(), "https://192.168.1.1/a.jpg"); !errors.Is(err, ErrCoverURL) {
		t.Fatalf("%v", err)
	}
	eq(t, "requests", hits.Load(), 0)
}

func TestFetchCoverForbiddenRedirect(t *testing.T) {
	f, hits := coverServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest", http.StatusFound)
	})
	if _, err := f.Fetch(ctx(), "https://fantlab.ru/a"); !errors.Is(err, ErrCoverURL) {
		t.Fatalf("%v", err)
	}
	eq(t, "requests", hits.Load(), 1)
}

func TestFetchCoverTooManyRedirects(t *testing.T) {
	f, hits := coverServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://archive.org/next", http.StatusFound)
	})
	if _, err := f.Fetch(ctx(), "https://archive.org/a"); !errors.Is(err, ErrCoverUnavailable) {
		t.Fatalf("%v", err)
	}
	eq(t, "requests", hits.Load(), coverRedirects+1)
}

func TestFetchCoverNotFound(t *testing.T) {
	f, _ := coverServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	if _, err := f.Fetch(ctx(), "https://fantlab.ru/a"); !errors.Is(err, ErrCoverUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestFetchCoverTooLarge(t *testing.T) {
	f, _ := coverServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", meta.MaxCover+1)))
	})
	if _, err := f.Fetch(ctx(), "https://fantlab.ru/a"); !errors.Is(err, ErrCoverUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestFetchCoverHonoursContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f, _ := coverServer(t, func(http.ResponseWriter, *http.Request) { <-release })
	c, cancel := context.WithCancel(ctx())
	cancel()
	if _, err := f.Fetch(c, "https://fantlab.ru/a"); !errors.Is(err, ErrCoverUnavailable) {
		t.Fatalf("%v", err)
	}
}
