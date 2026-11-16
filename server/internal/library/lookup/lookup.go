// Package lookup ищет метаданные книг во внешних каталогах. Запросы уходят только
// по явному действию администратора: из диалога метаданных или фоновым поиском,
// который администратор включил в настройках. Сбой одного каталога не мешает остальным.
package lookup

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

// Идентификаторы источников кандидата.
const (
	ProviderFile        = "file"
	ProviderFantLab     = "fantlab"
	ProviderOpenLibrary = "openlibrary"
	ProviderGoogle      = "google"
)

// Способ, которым найден кандидат.
const (
	MatchFile = "file"
	MatchISBN = "isbn"
	MatchText = "text"
)

// Состояние провайдера в ответе.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

const (
	// providerTimeout меньше общего срока записи сервера (30 с).
	providerTimeout = 12 * time.Second
	maxCandidates   = 5

	maxTitle       = 500
	maxAuthors     = 1000
	maxDescription = 20000
)

// Query — что искать: либо ISBN, либо Text. Language — двухбуквенный код языка книги.
// Background — фоновый поиск: опрашиваются только кешируемые каталоги, Google нет.
type Query struct {
	ISBN, Text, Language string
	Background           bool
}

// Candidate — книга, найденная провайдером. Пустая строка — значения нет. Partial —
// поиск удался, а детали нет: такой результат показывают, но не кешируют.
type Candidate struct {
	Provider    string   `json:"provider"`
	Match       string   `json:"match"`
	Title       string   `json:"title"`
	Authors     string   `json:"authors"`
	Series      string   `json:"series"`
	SeriesIndex *float64 `json:"series_index"`
	Language    string   `json:"language"`
	Year        *int     `json:"year"`
	Description string   `json:"description"`
	ISBN        string   `json:"isbn"`
	CoverURL    string   `json:"cover_url"`
	URL         string   `json:"url"`
	Partial     bool     `json:"-"`
}

// Provider — внешний каталог.
type Provider interface {
	ID() string
	// Search ищет по q.ISBN, если он задан, иначе по q.Text.
	Search(ctx context.Context, q Query) ([]Candidate, error)
}

// Status — итог опроса провайдера.
type Status struct {
	ID, Status string
}

// Result — кандидаты: совпадения по ISBN, затем текстовые, внутри групп — в порядке
// провайдеров.
type Result struct {
	Items     []Candidate
	Providers []Status
}

// Lookup опрашивает провайдеров.
type Lookup struct {
	providers []Provider
	cache     *cache
	timeout   time.Duration
	log       *slog.Logger
}

// New создаёт Lookup с FantLab и Open Library; Google Books — только при непустом googleKey.
func New(db *store.DB, googleKey string, log *slog.Logger) *Lookup {
	providers := []Provider{
		NewFantLab(http.DefaultClient, fantLabBase, fantLabGap),
		NewOpenLibrary(http.DefaultClient, openLibraryBase, openLibraryGap),
	}
	if googleKey != "" {
		providers = append(providers, NewGoogle(http.DefaultClient, googleBase, googleKey))
	}
	return NewWithProviders(providers, db, log)
}

// NewWithProviders создаёт Lookup с заданными провайдерами.
func NewWithProviders(providers []Provider, db *store.DB, log *slog.Logger) *Lookup {
	return &Lookup{providers: providers, cache: newCache(db), timeout: providerTimeout, log: log}
}

// Search опрашивает провайдеров параллельно. Ответ есть всегда: сбой отражается
// статусом провайдера. Фоновый поиск пропускает Google: его ответы нельзя хранить,
// и проход по библиотеке тратил бы квоту ключа.
func (l *Lookup) Search(ctx context.Context, q Query) Result {
	type outcome struct {
		items  []Candidate
		match  string
		status string
	}
	providers := l.providers
	if q.Background {
		providers = slices.DeleteFunc(slices.Clone(providers), func(p Provider) bool { return !cacheable(p) })
	}
	outcomes := make([]outcome, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Go(func() {
			items, match, err := l.ask(ctx, p, q)
			outcomes[i] = outcome{items: items, match: match, status: StatusOK}
			if err != nil {
				l.log.Warn("library: metadata provider failed", "provider", p.ID(), "error", err)
				outcomes[i].status = StatusError
			}
		})
	}
	wg.Wait()

	res := Result{Items: []Candidate{}, Providers: make([]Status, len(providers))}
	for i, p := range providers {
		res.Providers[i] = Status{ID: p.ID(), Status: outcomes[i].status}
	}
	for _, match := range []string{MatchISBN, MatchText} {
		for _, o := range outcomes {
			if o.match != match {
				continue
			}
			for _, c := range o.items[:min(len(o.items), maxCandidates)] {
				c.Match = match
				res.Items = append(res.Items, Clamp(c))
			}
		}
	}
	return res
}

// ask ищет у одного провайдера: сначала ISBN, при пустом результате — текст.
func (l *Lookup) ask(ctx context.Context, p Provider, q Query) ([]Candidate, string, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	if q.ISBN != "" {
		items, err := l.search(ctx, p, Query{ISBN: q.ISBN})
		if err != nil || len(items) > 0 {
			return items, MatchISBN, err
		}
	}
	if q.Text == "" {
		return nil, "", nil
	}
	items, err := l.search(ctx, p, Query{Text: q.Text, Language: q.Language})
	return items, MatchText, err
}

func (l *Lookup) search(ctx context.Context, p Provider, q Query) ([]Candidate, error) {
	cached := cacheable(p)
	key := cacheKey(q)
	if cached {
		items, ok, err := l.cache.get(ctx, p.ID(), key)
		if err != nil {
			l.log.Warn("library: metadata cache read failed", "error", err)
		} else if ok {
			return items, nil
		}
	}
	items, err := p.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	if cached && !anyPartial(items) {
		// Срок запроса мог выйти на последней детали: запись от него не зависит.
		if err := l.cache.put(context.WithoutCancel(ctx), p.ID(), key, items); err != nil {
			l.log.Warn("library: metadata cache write failed", "error", err)
		}
	}
	return items, nil
}

// cacheable — ответы провайдера можно кешировать. Условия Google запрещают хранить
// данные дольше заголовка ответа, а поиск по одной книге квоту не исчерпает.
func cacheable(p Provider) bool { return p.ID() != ProviderGoogle }

func anyPartial(items []Candidate) bool {
	for _, c := range items {
		if c.Partial {
			return true
		}
	}
	return false
}

func cacheKey(q Query) string {
	if q.ISBN != "" {
		return "isbn:" + q.ISBN
	}
	return "text:" + q.Language + ":" + meta.Fold(q.Text)
}

// Clamp обрезает поля кандидата до границ формы: кандидата всегда можно сохранить.
func Clamp(c Candidate) Candidate {
	c.Title = limitRunes(c.Title, maxTitle)
	c.Series = limitRunes(c.Series, maxTitle)
	c.Authors = limitRunes(c.Authors, maxAuthors)
	c.Description = limitRunes(c.Description, maxDescription)
	return c
}

func limitRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}
