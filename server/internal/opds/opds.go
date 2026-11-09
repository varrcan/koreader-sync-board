// Package opds — каталог OPDS 1.2 (Atom) под /opds для стандартных читалок:
// KOReader (opds.koplugin) и CrossPoint. Только чтение: лента книг, поиск,
// скачивание файлов и обложек, EPUB из FB2 по запросу.
//
// Вход — Basic с паролем устройства, той же учёткой, что у kosync и WebDAV.
// Проверка идёт до маршрутизации: без пароля неизвестный путь неотличим от
// известного. Адреса в лентах — пути от корня сервера: клиенты разрешают их
// относительно адреса ленты, поэтому публичный адрес серверу знать не нужно.
package opds

import (
	"encoding/xml"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
	"github.com/varrcan/koreader-sync-board/server/internal/deviceauth"
	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/convert"
)

// Prefix — путь корня каталога.
const Prefix = "/opds"

const (
	// pageSize — записей на страницу: KOReader подгружает rel="next" сам, а у
	// CrossPoint предел 62 записи на ленту.
	pageSize        = 30
	maxSearchLength = 200
)

// Library — библиотека, которую показывает каталог.
type Library struct {
	Store     *library.Store
	Covers    *library.Covers
	Converter *convert.Service
}

// Handler обслуживает префиксы /opds и /opds/.
type Handler struct {
	auth *deviceauth.Basic
	lib  Library
	log  *slog.Logger
	now  func() time.Time
	mux  *http.ServeMux
}

// New создаёт обработчик. ip определяет адрес клиента для ограничителя и лога.
func New(acc *accounts.Service, ip *clientip.Resolver, lib Library, log *slog.Logger) *Handler {
	h := &Handler{auth: deviceauth.New(acc, ip, log, "opds", false), lib: lib, log: log, now: time.Now, mux: http.NewServeMux()}
	// Шаблон GET принимает и HEAD: KOReader v2026.07 делает его перед GET ленты,
	// а другие методы ServeMux отвергает кодом 405 с заголовком Allow.
	h.mux.HandleFunc("GET "+Prefix, h.root)
	h.mux.HandleFunc("GET "+Prefix+"/{$}", h.root)
	h.mux.HandleFunc("GET "+Prefix+"/books", h.books)
	h.mux.HandleFunc("GET "+Prefix+"/search", h.search)
	h.mux.HandleFunc("GET "+Prefix+"/opensearch.xml", h.openSearch)
	h.mux.HandleFunc("GET "+Prefix+"/files/{id}", h.file)
	h.mux.HandleFunc("GET "+Prefix+"/files/{id}/epub", h.epub)
	h.mux.HandleFunc("GET "+Prefix+"/covers/{cover}", h.cover)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.auth.Authenticate(w, r); !ok {
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) root(w http.ResponseWriter, _ *http.Request) {
	h.writeXML(w, navigationType, rootFeed(h.now()))
}

func (h *Handler) books(w http.ResponseWriter, r *http.Request) {
	before, ok := parseBefore(w, r)
	if !ok {
		return
	}
	h.writeBooks(w, r, "", url.Values{}, feedMeta{id: "urn:ksb:opds:books", title: "All books", path: Prefix + "/books"}, before)
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	before, ok := parseBefore(w, r)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	if utf8.RuneCountInString(q) > maxSearchLength {
		http.Error(w, "q is too long", http.StatusBadRequest)
		return
	}
	h.writeBooks(w, r, q, url.Values{"q": {q}}, feedMeta{id: "urn:ksb:opds:search", title: "Search results", path: Prefix + "/search"}, before)
}

func (h *Handler) writeBooks(w http.ResponseWriter, r *http.Request, q string, query url.Values, m feedMeta, before int64) {
	page, err := h.lib.Store.Feed(r.Context(), q, before, pageSize)
	if err != nil {
		h.internal(w, r, "read feed", err)
		return
	}
	m.query, m.before = query, before
	h.writeXML(w, acquisitionType, bookFeed(m, page, h.now()))
}

func (h *Handler) openSearch(w http.ResponseWriter, _ *http.Request) {
	h.writeXML(w, openSearchType, openSearchDocument())
}

// parseBefore разбирает курсор; без параметра — начало списка.
func parseBefore(w http.ResponseWriter, r *http.Request) (int64, bool) {
	query := r.URL.Query()
	if !query.Has("before") {
		return 0, true
	}
	before, err := strconv.ParseInt(query.Get("before"), 10, 64)
	if err != nil || before <= 0 {
		http.Error(w, "invalid before", http.StatusBadRequest)
		return 0, false
	}
	return before, true
}

// writeXML отдаёт документ без предисловия: KOReader v2026.07.1 разбирает Atom,
// только если первый байт тела — «<». Сборка в памяти даёт Content-Length и не
// оставляет полуотправленной ленты при ошибке.
func (h *Handler) writeXML(w http.ResponseWriter, contentType string, doc any) {
	body, err := marshal(doc)
	if err != nil {
		h.log.Error("opds: marshal", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

func marshal(doc any) ([]byte, error) {
	body, err := xml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, op string, err error) {
	if r.Context().Err() != nil {
		return
	}
	h.log.Error("opds: "+op, "err", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
