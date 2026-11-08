package lookup

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

const (
	fantLabBase = "https://api.fantlab.ru"
	fantLabGap  = 100 * time.Millisecond
	fantLabSite = "https://fantlab.ru"
)

// fantLabBookTypes — типы произведений, которые бывают книгой. В /search-works лежат
// и статьи, рецензии, сценарии, интервью, отрывки, циклы, журналы. Состав сверен с
// name_show_im реальных ответов.
var fantLabBookTypes = map[string]bool{
	"роман": true, "роман-эпопея": true, "повесть": true, "рассказ": true, "сборник": true, "антология": true,
	"сказка": true, "пьеса": true, "поэма": true, "биография": true, "монография": true,
	"документальное произведение": true, "графический роман": true, "комикс": true,
}

var bbCode = regexp.MustCompile(`\[/?[a-zA-Z_]+(=[^\]]*)?\]`)

// bbText убирает BB-коды и HTML: API отдаёт разметку сайта.
func bbText(s string) string { return meta.PlainText(bbCode.ReplaceAllString(s, "")) }

// FantLab — каталог fantlab.ru, API v0.9 без ключа.
type FantLab struct{ c *client }

// NewFantLab создаёт провайдера.
func NewFantLab(h *http.Client, base string, gap time.Duration) *FantLab {
	return &FantLab{c: newClient(h, base, gap)}
}

// ID реализует Provider.
func (f *FantLab) ID() string { return ProviderFantLab }

// Search реализует Provider.
func (f *FantLab) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if q.ISBN != "" {
		return f.byISBN(ctx, q.ISBN)
	}
	return f.byText(ctx, q.Text)
}

type fantLabPerson struct {
	Name string `json:"name"`
}

type fantLabEditionMatch struct {
	EditionID flexInt `json:"edition_id"`
	Name      string  `json:"name"`
	Autors    string  `json:"autors"`
	ISBN      string  `json:"isbn"`
	Year      flexInt `json:"year"`
}

func (f *FantLab) searchEditions(ctx context.Context, isbn string) ([]fantLabEditionMatch, error) {
	var res struct {
		Matches []fantLabEditionMatch `json:"matches"`
	}
	err := f.c.getJSON(ctx, "/search-editions", url.Values{"q": {isbn}, "page": {"1"}}, &res)
	return res.Matches, err
}

// byISBN берёт только первое издание: все результаты по ISBN — одно издание.
func (f *FantLab) byISBN(ctx context.Context, isbn string) ([]Candidate, error) {
	matches, err := f.searchEditions(ctx, isbn)
	if err != nil {
		return nil, err
	}
	// Часть изданий хранится только с ISBN-10.
	if short := isbn10(isbn); len(matches) == 0 && short != "" {
		if matches, err = f.searchEditions(ctx, short); err != nil {
			return nil, err
		}
	}
	if len(matches) == 0 {
		return []Candidate{}, nil
	}
	m := matches[0]
	c := Candidate{
		Provider: ProviderFantLab, Title: bbText(m.Name), Authors: bbText(m.Autors),
		ISBN: meta.NormalizeISBN(m.ISBN), Year: m.Year.year(),
		URL: fmt.Sprintf("%s/edition/%d", fantLabSite, m.EditionID),
	}
	var e struct {
		Name     string `json:"edition_name"`
		Creators struct {
			Authors []fantLabPerson `json:"authors"`
		} `json:"creators"`
		ISBNs    []string `json:"isbns"`
		Year     flexInt  `json:"year"`
		LangCode string   `json:"lang_code"`
		Image    string   `json:"image"`
	}
	if err := f.c.getJSON(ctx, fmt.Sprintf("/edition/%d", m.EditionID), nil, &e); err != nil {
		c.Partial = true
		return []Candidate{c}, nil
	}
	// description издания — библиографическая заметка, а series — серия издательства:
	// оба поля не про книгу.
	if name := bbText(e.Name); name != "" {
		c.Title = name
	}
	if names := joinPeople(e.Creators.Authors); names != "" {
		c.Authors = names
	}
	for _, s := range e.ISBNs {
		if n := meta.NormalizeISBN(s); n != "" {
			c.ISBN = n
			break
		}
	}
	if y := e.Year.year(); y != nil {
		c.Year = y
	}
	c.Language = e.LangCode
	c.CoverURL = fantLabImage(e.Image)

	works, err := f.searchWorks(ctx, c.Title)
	if err != nil {
		c.Partial = true
		return []Candidate{c}, nil
	}
	for _, w := range works {
		if fantLabBookTypes[w.Type] && meta.Fold(w.title()) == meta.Fold(c.Title) {
			d, err := f.work(ctx, w.ID)
			if err != nil {
				c.Partial = true
				break
			}
			c.Description, c.Series = d.description(), d.saga()
			break
		}
	}
	return []Candidate{c}, nil
}

func joinPeople(people []fantLabPerson) string {
	var out []string
	for _, p := range people {
		if n := bbText(p.Name); n != "" {
			out = append(out, n)
		}
	}
	return strings.Join(out, ", ")
}

// isbn10 переводит ISBN-13 с префиксом 978 в ISBN-10; иначе — пустая строка.
func isbn10(s string) string {
	if len(s) != 13 || !strings.HasPrefix(s, "978") {
		return ""
	}
	body := s[3:12]
	sum := 0
	for i, c := range []byte(body) {
		sum += int(c-'0') * (10 - i)
	}
	check := (11 - sum%11) % 11
	if check == 10 {
		return body + "X"
	}
	return body + string(rune('0'+check))
}

type fantLabWork struct {
	ID      flexInt `json:"work_id"`
	Name    string  `json:"name"`
	RusName string  `json:"rusname"`
	Type    string  `json:"name_show_im"`
	Authors string  `json:"all_autor_rusname"` //nolint:misspell // так поле называет API
	AltAuth string  `json:"all_autor_name"`    //nolint:misspell // так поле называет API
	Year    flexInt `json:"year"`
}

func (w fantLabWork) title() string {
	if w.RusName != "" {
		return bbText(w.RusName)
	}
	return bbText(w.Name)
}

func (f *FantLab) searchWorks(ctx context.Context, text string) ([]fantLabWork, error) {
	var works []fantLabWork
	err := f.c.getJSON(ctx, "/search-works", url.Values{"q": {text}, "page": {"1"}, "onlymatches": {"1"}}, &works)
	return works, err
}

type fantLabDetails struct {
	Description string `json:"work_description"`
	Image       string `json:"image"`
	RootSaga    []struct {
		Name string `json:"work_name"`
	} `json:"work_root_saga"`
}

func (d fantLabDetails) description() string { return bbText(d.Description) }

func (d fantLabDetails) saga() string {
	if len(d.RootSaga) == 0 {
		return ""
	}
	return bbText(d.RootSaga[0].Name)
}

func (f *FantLab) work(ctx context.Context, id flexInt) (fantLabDetails, error) {
	var d fantLabDetails
	err := f.c.getJSON(ctx, fmt.Sprintf("/work/%d/extended", id), nil, &d)
	return d, err
}

func (f *FantLab) byText(ctx context.Context, text string) ([]Candidate, error) {
	works, err := f.searchWorks(ctx, text)
	if err != nil {
		return nil, err
	}
	out := []Candidate{}
	for _, w := range works {
		if !fantLabBookTypes[w.Type] {
			continue
		}
		if len(out) == maxCandidates {
			break
		}
		// Language пуст: код языка произведения — язык оригинала, а не издания.
		c := Candidate{
			Provider: ProviderFantLab, Title: w.title(), Authors: bbText(w.Authors), Year: w.Year.year(),
			URL: fmt.Sprintf("%s/work/%d", fantLabSite, w.ID),
		}
		if c.Authors == "" {
			c.Authors = bbText(w.AltAuth)
		}
		if d, err := f.work(ctx, w.ID); err != nil {
			c.Partial = true
		} else {
			c.Description, c.Series, c.CoverURL = d.description(), d.saga(), fantLabImage(d.Image)
		}
		out = append(out, c)
	}
	return out, nil
}

// fantLabImage дополняет относительный путь обложки хостом сайта.
func fantLabImage(path string) string {
	if path == "" {
		return ""
	}
	return fantLabSite + path
}
