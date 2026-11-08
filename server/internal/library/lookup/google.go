package lookup

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

const googleBase = "https://www.googleapis.com"

// Google — Google Books, только с ключом: без него все запросы отвечают 429.
type Google struct {
	c   *client
	key string
}

// NewGoogle создаёт провайдера.
func NewGoogle(h *http.Client, base, key string) *Google {
	return &Google{c: newClient(h, base, 0), key: key}
}

// ID реализует Provider.
func (g *Google) ID() string { return ProviderGoogle }

// Search реализует Provider. Без langRestrict: иначе теряются издания с пустым языком.
func (g *Google) Search(ctx context.Context, q Query) ([]Candidate, error) {
	text := q.Text
	if q.ISBN != "" {
		text = "isbn:" + q.ISBN
	}
	params := url.Values{"q": {text}, "maxResults": {strconv.Itoa(maxCandidates)}, "key": {g.key}}
	var res struct {
		Items []struct {
			Info struct {
				Title       string   `json:"title"`
				Authors     []string `json:"authors"`
				Published   string   `json:"publishedDate"`
				Description string   `json:"description"`
				Language    string   `json:"language"`
				Identifiers []struct {
					Identifier string `json:"identifier"`
				} `json:"industryIdentifiers"`
				Images struct {
					Thumbnail string `json:"thumbnail"`
					Small     string `json:"smallThumbnail"`
				} `json:"imageLinks"`
				Canonical string `json:"canonicalVolumeLink"`
				InfoLink  string `json:"infoLink"`
			} `json:"volumeInfo"`
		} `json:"items"`
	}
	if err := g.c.getJSON(ctx, "/books/v1/volumes", params, &res); err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(res.Items))
	for _, it := range res.Items {
		v := it.Info
		var isbns []string
		for _, id := range v.Identifiers {
			isbns = append(isbns, id.Identifier)
		}
		c := Candidate{
			Provider: ProviderGoogle, Title: strings.TrimSpace(v.Title), Authors: strings.Join(v.Authors, ", "),
			Description: meta.PlainText(v.Description), Language: v.Language, ISBN: preferISBN13(isbns),
			Year: leadingYear(v.Published), URL: toHTTPS(firstNonEmpty(v.Canonical, v.InfoLink)),
			CoverURL: toHTTPS(firstNonEmpty(v.Images.Thumbnail, v.Images.Small)),
		}
		out = append(out, c)
	}
	return out, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func toHTTPS(u string) string {
	if rest, ok := strings.CutPrefix(u, "http:"); ok {
		return "https:" + rest
	}
	return u
}

// leadingYear — год из даты вида 2005, 2005-11 или 2005-11-15.
func leadingYear(date string) *int {
	if len(date) < 4 {
		return nil
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil || y <= 0 {
		return nil
	}
	return &y
}
