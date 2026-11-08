package lookup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	userAgent = "koreader-sync-board (+https://github.com/varrcan/koreader-sync-board)"
	// maxResponse с запасом вмещает /work/{id}/extended FantLab: у популярных книг
	// это сотни килобайт.
	maxResponse = 8 << 20
)

// client — запросы к одному провайдеру: строго по одному и с паузой, поэтому двое
// администраторов вместе не нарушат лимит каталога.
type client struct {
	http *http.Client
	base string
	gap  time.Duration
	slot chan struct{} // очередь с выходом по контексту, в отличие от мьютекса
	last time.Time     // защищён slot
}

func newClient(h *http.Client, base string, gap time.Duration) *client {
	return &client{http: h, base: strings.TrimRight(base, "/"), gap: gap, slot: make(chan struct{}, 1)}
}

// getJSON разбирает ответ path в out. Текст ошибки не содержит URL: в запросе
// Google лежит ключ.
func (c *client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	select {
	case c.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.slot }()
	if wait := c.gap - time.Since(c.last); wait > 0 && !c.last.IsZero() {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.last = time.Now()

	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	body, err := get(ctx, c.http, target, maxResponse)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

var errTooLarge = errors.New("response exceeds the size limit")

func get(ctx context.Context, h *http.Client, target string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errors.New("build request")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, image/*")
	resp, err := h.Do(req)
	if err != nil {
		return nil, stripURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, stripURL(err)
	}
	if int64(len(body)) > limit {
		return nil, errTooLarge
	}
	return body, nil
}

// stripURL снимает с ошибки net/http адрес запроса, сохраняя причину.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// flexInt — число, которое каталог присылает то числом, то строкой, то null.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// Год бывает текстом вроде «1989-90»: это не повод терять весь ответ.
		*f = 0
		return nil
	}
	*f = flexInt(v)
	return nil
}

func (f flexInt) year() *int {
	if f <= 0 {
		return nil
	}
	y := int(f)
	return &y
}
