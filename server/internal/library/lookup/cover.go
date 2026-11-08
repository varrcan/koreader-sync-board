package lookup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

const (
	coverTimeout   = 20 * time.Second
	coverRedirects = 3
)

var (
	// ErrCoverURL — адрес обложки вне разрешённых хостов.
	ErrCoverURL = errors.New("cover url is not allowed")
	// ErrCoverUnavailable — изображение не удалось скачать.
	ErrCoverUnavailable = errors.New("cover is unavailable")
)

// CoverFetcher скачивает обложки кандидатов.
type CoverFetcher struct{ client *http.Client }

// NewCoverFetcher создаёт CoverFetcher.
func NewCoverFetcher() *CoverFetcher { return newCoverFetcher(http.DefaultTransport) }

func newCoverFetcher(rt http.RoundTripper) *CoverFetcher {
	return &CoverFetcher{client: &http.Client{
		Transport: rt,
		// Адрес присылает клиент, поэтому проверяется и каждый редирект: Open Library
		// отправляет на archive.org, а тот — на ia*.us.archive.org.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > coverRedirects {
				return fmt.Errorf("%w: too many redirects", ErrCoverUnavailable)
			}
			if !allowedCoverURL(req.URL) {
				return ErrCoverURL
			}
			return nil
		},
	}}
}

// ValidCoverURL сообщает, разрешён ли адрес обложки.
func ValidCoverURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && allowedCoverURL(u)
}

func allowedCoverURL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "covers.openlibrary.org", "fantlab.ru", "books.google.com", "archive.org":
		return true
	}
	return strings.HasSuffix(host, ".archive.org") || strings.HasSuffix(host, ".googleusercontent.com")
}

// Fetch скачивает изображение. ErrCoverURL — адрес запрещён, ErrCoverUnavailable —
// всё остальное.
func (f *CoverFetcher) Fetch(ctx context.Context, raw string) ([]byte, error) {
	if !ValidCoverURL(raw) {
		return nil, ErrCoverURL
	}
	ctx, cancel := context.WithTimeout(ctx, coverTimeout)
	defer cancel()
	body, err := get(ctx, f.client, raw, meta.MaxCover)
	switch {
	case err == nil:
		return body, nil
	case errors.Is(err, ErrCoverURL):
		return nil, ErrCoverURL
	default:
		return nil, fmt.Errorf("%w: %w", ErrCoverUnavailable, err)
	}
}
