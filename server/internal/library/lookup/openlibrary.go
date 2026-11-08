package lookup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

const (
	openLibraryBase = "https://openlibrary.org"
	// Лимит 3 rps требует email или телефон в User-Agent, которых у self-hosted
	// сервера нет, поэтому действует лимит 1 rps для неопознанных.
	openLibraryGap = 1100 * time.Millisecond
	// openLibraryMax держит цепочку (поиск + детали) в таймауте провайдера при паузе 1,1 с.
	openLibraryMax    = 3
	openLibraryFields = "key,title,author_name,first_publish_year,cover_i,series_name,series_position," +
		"editions,editions.key,editions.title,editions.isbn,editions.language,editions.cover_i"
)

// openLibraryLanguages переводит коды MARC, неизвестный язык остаётся пустым.
var openLibraryLanguages = map[string]string{
	"rus": "ru", "eng": "en", "ukr": "uk", "bel": "be", "ger": "de", "fre": "fr", "spa": "es", "ita": "it",
	"pol": "pl", "por": "pt", "jpn": "ja", "chi": "zh", "cze": "cs", "bul": "bg", "dut": "nl", "swe": "sv",
}

var (
	markdownLink = regexp.MustCompile(`\[([^\]]*)\](\([^)]*\)|\[[^\]]*\])`)
	// markdownRef — сноска «[1]: https://…», на которую ссылаются «[текст][1]».
	markdownRef = regexp.MustCompile(`(?m)^\s*\[[^\]]+\]:\s*\S+.*$`)
)

// OpenLibrary — каталог openlibrary.org без ключа.
type OpenLibrary struct{ c *client }

// NewOpenLibrary создаёт провайдера.
func NewOpenLibrary(h *http.Client, base string, gap time.Duration) *OpenLibrary {
	return &OpenLibrary{c: newClient(h, base, gap)}
}

// ID реализует Provider.
func (o *OpenLibrary) ID() string { return ProviderOpenLibrary }

type openLibraryDoc struct {
	Key            string   `json:"key"`
	Title          string   `json:"title"`
	Authors        []string `json:"author_name"`
	Year           flexInt  `json:"first_publish_year"`
	CoverID        flexInt  `json:"cover_i"`
	SeriesName     []string `json:"series_name"`
	SeriesPosition []string `json:"series_position"`
	Editions       struct {
		Docs []struct {
			Key      string   `json:"key"`
			Title    string   `json:"title"`
			CoverID  flexInt  `json:"cover_i"`
			Language []string `json:"language"`
			ISBN     []string `json:"isbn"`
		} `json:"docs"`
	} `json:"editions"`
}

// Search реализует Provider. Параметры title= и author= на кириллице дают 0
// результатов, поэтому всё идёт через q.
func (o *OpenLibrary) Search(ctx context.Context, q Query) ([]Candidate, error) {
	params := url.Values{"fields": {openLibraryFields}, "limit": {strconv.Itoa(openLibraryMax)}}
	if q.ISBN != "" {
		params.Set("q", "isbn:"+q.ISBN)
	} else {
		params.Set("q", q.Text)
		if q.Language != "" {
			params.Set("lang", q.Language)
		}
	}
	var res struct {
		Docs []openLibraryDoc `json:"docs"`
	}
	if err := o.c.getJSON(ctx, "/search.json", params, &res); err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(res.Docs))
	for _, d := range res.Docs[:min(len(res.Docs), openLibraryMax)] {
		c := candidateFromDoc(d)
		if description, err := o.description(ctx, d.Key); err != nil {
			c.Partial = true
		} else {
			c.Description = description
		}
		out = append(out, c)
	}
	return out, nil
}

func candidateFromDoc(d openLibraryDoc) Candidate {
	c := Candidate{
		Provider: ProviderOpenLibrary, Title: strings.TrimSpace(d.Title), Authors: strings.Join(d.Authors, ", "),
		Year: d.Year.year(), URL: "https://openlibrary.org" + d.Key,
	}
	if len(d.SeriesName) > 0 {
		c.Series = d.SeriesName[0]
		if len(d.SeriesPosition) > 0 {
			if v, err := strconv.ParseFloat(strings.TrimSpace(d.SeriesPosition[0]), 64); err == nil {
				c.SeriesIndex = &v
			}
		}
	}
	cover := d.CoverID
	if len(d.Editions.Docs) > 0 {
		e := d.Editions.Docs[0]
		if t := strings.TrimSpace(e.Title); t != "" {
			c.Title = t
		}
		if e.CoverID > 0 {
			cover = e.CoverID
		}
		if len(e.Language) > 0 {
			c.Language = openLibraryLanguages[e.Language[0]]
		}
		c.ISBN = preferISBN13(e.ISBN)
		if e.Key != "" {
			c.URL = "https://openlibrary.org" + e.Key
		}
	}
	if cover > 0 {
		// default=false: без обложки — 404, а не картинка-заглушка.
		c.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg?default=false", cover)
	}
	return c
}

func preferISBN13(list []string) string {
	var first string
	for _, s := range list {
		n := meta.NormalizeISBN(s)
		if len(n) == 13 {
			return n
		}
		if first == "" {
			first = n
		}
	}
	return first
}

func (o *OpenLibrary) description(ctx context.Context, key string) (string, error) {
	if !strings.HasPrefix(key, "/works/") {
		return "", fmt.Errorf("unexpected work key")
	}
	var w struct {
		Description json.RawMessage `json:"description"`
	}
	if err := o.c.getJSON(ctx, key+".json", nil, &w); err != nil {
		return "", err
	}
	return meta.PlainText(markdownLink.ReplaceAllString(markdownRef.ReplaceAllString(rawText(w.Description), ""), "$1")), nil
}

// rawText — поле, которое бывает строкой, объектом {type, value} или отсутствует.
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.Value
	}
	return ""
}
