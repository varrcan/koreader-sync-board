// Package suggest ищет во внешних каталогах метаданные для книг библиотеки без
// обложки или аннотации. Найденное не применяется: книга получает отметку «есть
// предложение», а кандидатов администратор смотрит в диалоге метаданных — они уже в
// кеше поиска.
package suggest

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/lookup"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

const (
	// pause — между книгами, по которым шли запросы, поверх пауз провайдеров: фон не
	// должен выглядеть как выкачивание каталога. 1000 книг — 1,5–3 часа.
	pause = 5 * time.Second
	// interval — повтор без внешнего события: неудавшиеся проверки ждут SuggestRetry.
	interval = time.Hour
)

// Settings — включён ли фоновый поиск.
type Settings interface {
	MetadataSuggest(ctx context.Context) (bool, error)
}

// Searcher ищет кандидатов во внешних каталогах.
type Searcher interface {
	Search(ctx context.Context, q lookup.Query) lookup.Result
}

// Worker проверяет книги по одной: при старте, по Kick и раз в interval.
type Worker struct {
	store    *library.Store
	files    library.FileReader
	lookup   Searcher
	settings Settings
	log      *slog.Logger
	kick     chan struct{}

	pause, interval time.Duration
	now             func() time.Time
}

// New создаёт Worker. lookup должен быть общим с панелью: очередь и паузы
// провайдеров тогда общие, и ручной поиск вместе с фоном лимит не превысят.
func New(st *library.Store, files library.FileReader, lookup Searcher, settings Settings, log *slog.Logger) *Worker {
	return &Worker{
		store: st, files: files, lookup: lookup, settings: settings, log: log, kick: make(chan struct{}, 1),
		pause: pause, interval: interval, now: time.Now,
	}
}

// Kick будит обработчик, не блокируясь; вызовы во время прохода дают один следующий.
func (w *Worker) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Run работает до отмены ctx. Первый проход — сразу: без периодического скана
// (KSB_LIBRARY_SCAN_INTERVAL=0) сигнала о конце скана при старте не будет.
func (w *Worker) Run(ctx context.Context) {
	w.Kick()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		case <-t.C:
		}
		w.pass(ctx)
	}
}

// pass проверяет очередь от начала до конца. Курсор не возвращается назад: книга,
// проверка которой только что не удалась, в этом проходе снова не берётся.
func (w *Worker) pass(ctx context.Context) {
	var (
		after                  int64
		checked, found, failed int
	)
	defer func() {
		if checked > 0 {
			w.log.Info("library: metadata suggestions finished", "checked", checked, "found", found, "failed", failed)
		}
	}()
	for {
		// Перед каждой книгой: выключение останавливает поиск сразу.
		on, err := w.settings.MetadataSuggest(ctx)
		if err != nil || !on {
			w.warn(ctx, err)
			return
		}
		id, err := w.store.NextSuggestCandidate(ctx, after, w.now().Add(-library.SuggestRetry))
		if err != nil || id == 0 {
			w.warn(ctx, err)
			return
		}
		after = id
		state, asked, err := w.check(ctx, id)
		if ctx.Err() != nil {
			// Прерванная книга остаётся в очереди.
			return
		}
		if err != nil {
			w.warn(ctx, err)
			return
		}
		if state == "" {
			continue
		}
		checked++
		switch state {
		case library.SuggestFound:
			found++
		case library.SuggestFailed:
			failed++
		}
		if asked && !w.sleep(ctx) {
			return
		}
	}
}

// check проверяет книгу и записывает итог; asked — были запросы к провайдерам.
// Удалённая за это время книга пропускается.
func (w *Worker) check(ctx context.Context, id int64) (state string, asked bool, err error) {
	d, err := w.store.Book(ctx, id, 0, false)
	if errors.Is(err, library.ErrBookNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var metaUpdated int64
	if d.Metadata != nil {
		metaUpdated = d.Metadata.UpdatedAt.Unix()
	}
	files, err := w.store.ParseableFiles(ctx, id)
	if err != nil {
		return "", false, err
	}
	fromFiles, complete := w.files.ReadAll(files)
	g := gaps{cover: d.Book.Cover == nil, description: (d.Description == nil || *d.Description == "") && fromFiles.Description == ""}
	switch {
	case !complete:
		// Отключённый NAS дал бы бессрочное ложное «нет аннотации» и поиск без ISBN.
		state = library.SuggestFailed
	case !g.cover && !g.description:
		state = library.SuggestComplete
	default:
		asked = true
		res := w.lookup.Search(ctx, lookup.Query{
			ISBN: fromFiles.ISBN, Text: library.AutoQuery(d.Book), Language: library.BookLanguage(d.Book), Background: true,
		})
		if ctx.Err() != nil {
			return "", asked, ctx.Err()
		}
		state = evaluate(res, g, *d.Book.Title)
	}
	return state, asked, w.store.RecordSuggestCheck(ctx, id, state, metaUpdated)
}

// gaps — чего у книги нет ни в правке, ни в файлах.
type gaps struct{ cover, description bool }

// evaluate — итог поиска. Сбой каталога и кандидат без деталей — неудача: неполный
// ответ не кешируется, и фон не должен делать «предложений нет» бессрочным, когда
// FantLab, главный каталог русских книг, просто не ответил.
func evaluate(res lookup.Result, g gaps, title string) string {
	for _, p := range res.Providers {
		if p.Status != lookup.StatusOK {
			return library.SuggestFailed
		}
	}
	for _, c := range res.Items {
		if c.Partial {
			return library.SuggestFailed
		}
	}
	title = meta.Fold(title)
	for _, c := range res.Items {
		// Поиск по тексту путает издания и одноимённые книги: без ISBN нужно точное
		// название, а отметка лишь зовёт посмотреть.
		same := c.Match == lookup.MatchISBN || meta.Fold(c.Title) == title
		fills := (g.cover && c.CoverURL != "") || (g.description && c.Description != "")
		if same && fills {
			return library.SuggestFound
		}
	}
	return library.SuggestNone
}

func (w *Worker) sleep(ctx context.Context) bool {
	t := time.NewTimer(w.pause)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (w *Worker) warn(ctx context.Context, err error) {
	if err != nil && ctx.Err() == nil {
		w.log.Warn("library: metadata suggestions failed", "error", err)
	}
}
