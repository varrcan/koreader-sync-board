// Package webdav — минимальный WebDAV под /dav/ для облачной синхронизации
// KOReader (cloudstorage.koplugin, Cloud:sync): PROPFIND, GET/HEAD, PUT с If-Match.
//
// Корень /dav/ — единственная папка, набор файлов в нём определяется пользователем,
// под которым выполнен вход. Вход — Basic с паролем устройства, той же учёткой,
// что и kosync.
//
// Свой обработчик, а не golang.org/x/net/webdav: тот не проверяет If-Match при PUT,
// а без 412 цикл Cloud:sync не повторяется, и вторая читалка затирает выгрузку
// первой. Сравнение условия и запись здесь идут в одной транзакции писателя.
package webdav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/authlog"
	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics"
)

// Prefix — путь корня WebDAV.
const Prefix = "/dav"

const allow = "OPTIONS, PROPFIND, GET, HEAD, PUT"

// transferTimeout — срок чтения и записи для передачи файла. Сроки сервера (30 с)
// рассчитаны на kosync, а многомегабайтный снимок по Wi-Fi читалки идёт дольше.
const transferTimeout = 5 * time.Minute

// maxPropfindBody ограничивает тело PROPFIND: KOReader шлёт пару сотен байт.
const maxPropfindBody = 64 << 10

// Handler обслуживает префикс /dav/.
type Handler struct {
	accounts *accounts.Service
	stats    *statistics.Store
	ip       *clientip.Resolver
	log      *slog.Logger

	// beforeWrite вызывается между проверкой входа и записью PUT (для тестов).
	beforeWrite func()
}

// New создаёт обработчик. ip определяет адрес клиента для ограничителя и лога.
func New(acc *accounts.Service, stats *statistics.Store, ip *clientip.Resolver, log *slog.Logger) *Handler {
	return &Handler{accounts: acc, stats: stats, ip: ip, log: log}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	name, root, ok := resource(r.URL.Path)
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("DAV", "1")
		w.Header().Set("Allow", allow)
		w.WriteHeader(http.StatusOK)
	case "PROPFIND":
		h.propfind(w, r, p, name, root, ok)
	case http.MethodGet, http.MethodHead:
		if root {
			methodNotAllowed(w)
			return
		}
		h.get(w, r, p, name, ok)
	case http.MethodPut:
		if !ok || root {
			http.Error(w, "only "+statistics.FileStatistics+" and "+statistics.FileVocabulary+
				" can be stored in "+Prefix+"/", http.StatusForbidden)
			return
		}
		h.put(w, r, p, name)
	default:
		methodNotAllowed(w)
	}
}

// resource разбирает путь: root — корень, иначе name — имя файла в корне. ok
// ложно для подпутей и имён вне белого списка.
func resource(path string) (name string, root, ok bool) {
	rest, found := strings.CutPrefix(path, Prefix)
	if !found {
		return "", false, false
	}
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return "", true, true
	}
	return rest, false, statistics.Allowed(rest)
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// authenticate проверяет Basic с паролем устройства. Пароль превращается в ключ
// kosync (md5), поэтому проверка, кеш и ограничитель — те же, что у kosync.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (accounts.Principal, bool) {
	client := h.ip.Addr(r)
	user, pass, ok := r.BasicAuth()
	if !ok {
		authlog.Failure(h.log, "webdav", r, client, "", "missing basic credentials")
		unauthorized(w)
		return accounts.Principal{}, false
	}
	p, err := h.accounts.Authenticate(r.Context(), user, accounts.KeyFromPassword(pass), client)
	var rl *accounts.RateLimitedError
	switch {
	case err == nil:
		return p, true
	case errors.As(err, &rl):
		// Начало блокировки пишет ограничитель, отдельные отказы не логируются.
		w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(rl.RetryAfter.Seconds())), 10))
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
	case errors.Is(err, accounts.ErrUnauthorized):
		// Повтор недавно отвергнутого ключа — отозванная читалка; fail2ban по
		// таким строкам забанил бы её домашнюю сеть.
		if !errors.Is(err, accounts.ErrRepeated) {
			authlog.Failure(h.log, "webdav", r, client, user, err.Error())
		}
		unauthorized(w)
	default:
		h.log.Error("webdav: authenticate", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
	return accounts.Principal{}, false
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="koreader-sync-board"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// extendDeadlines продлевает сроки чтения и записи соединения для передачи файла.
// Одного срока чтения мало: срок записи net/http отсчитывает от чтения заголовков,
// и после долгой выгрузки ответ не дошёл бы до клиента, хотя файл уже записан.
// ResponseRecorder сроков не поддерживает, ошибка не важна.
func extendDeadlines(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(transferTimeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, p accounts.Principal, name string, ok bool) {
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := h.stats.Get(r.Context(), p.UserID, name)
	if errors.Is(err, statistics.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.internal(w, "get", err)
		return
	}
	extendDeadlines(w)
	// ETag обязателен: без него KOReader шлёт PUT без If-Match, получает 412 и
	// повторяет цикл бесконечно.
	w.Header().Set("ETag", f.ETag)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, f.ModifiedAt, bytes.NewReader(f.Content))
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request, p accounts.Principal, name string) {
	if r.ContentLength > statistics.MaxSize {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	extendDeadlines(w)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, statistics.MaxSize))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		}
		return
	}
	if h.beforeWrite != nil {
		h.beforeWrite()
	}
	f, created, err := h.stats.Save(r.Context(), p.UserID, &p, name, body, condition(r))
	switch {
	case err == nil:
		w.Header().Set("ETag", f.ETag)
		if created {
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	case errors.Is(err, statistics.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, statistics.ErrPrecondition):
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
	case errors.Is(err, accounts.ErrUnauthorized):
		authlog.Failure(h.log, "webdav", r, h.ip.Addr(r), p.Username, "credential changed before write")
		unauthorized(w)
	default:
		h.internal(w, "put", err)
	}
}

// condition — условие записи из If-Match. Без заголовка PUT разрешён только для
// отсутствующего файла (как If-None-Match: *): KOReader шлёт PUT без If-Match лишь
// после 404 на GET, и если файл к этому времени появился, две читалки
// синхронизируются впервые одновременно. Перезапись оставила бы снимок второй, а
// первая при следующей синхронизации удалила бы у себя свои книги; 412 заставляет
// её повторить цикл со слиянием.
func condition(r *http.Request) statistics.Cond {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		return statistics.Cond{}
	}
	match := []string{}
	for _, v := range values {
		for tag := range strings.SplitSeq(v, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				match = append(match, tag)
			}
		}
	}
	return statistics.Cond{Match: match}
}

func (h *Handler) internal(w http.ResponseWriter, op string, err error) {
	h.log.Error("webdav: "+op, "err", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

type multistatus struct {
	XMLName   xml.Name   `xml:"DAV: multistatus"`
	Responses []response `xml:"response"`
}

type response struct {
	Href     string   `xml:"href"`
	Propstat propstat `xml:"propstat"`
}

type propstat struct {
	Prop   prop   `xml:"prop"`
	Status string `xml:"status"`
}

type prop struct {
	ResourceType  resourceType `xml:"resourcetype"`
	ContentLength *int64       `xml:"getcontentlength,omitempty"`
	LastModified  string       `xml:"getlastmodified,omitempty"`
	ETag          string       `xml:"getetag,omitempty"`
}

type resourceType struct {
	Collection *struct{} `xml:"collection,omitempty"`
}

// propfind отвечает на листинг: Depth 0 — только сам ресурс, 1 и infinity
// (и без заголовка) — папка и её файлы. Тело запроса не разбирается: KOReader
// просит resourcetype, getcontentlength и getlastmodified, отдаются всегда они и
// getetag. href папки — путь из запроса: по нему KOReader узнаёт текущую папку.
func (h *Handler) propfind(w http.ResponseWriter, r *http.Request, p accounts.Principal, name string, root, ok bool) {
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxPropfindBody))
	href := r.URL.EscapedPath()
	var ms multistatus
	if root {
		ms.Responses = append(ms.Responses, response{Href: href, Propstat: okProps(prop{
			ResourceType: resourceType{Collection: &struct{}{}},
		})})
		if r.Header.Get("Depth") != "0" {
			files, err := h.stats.List(r.Context(), p.UserID)
			if err != nil {
				h.internal(w, "list", err)
				return
			}
			dir := strings.TrimSuffix(href, "/") + "/"
			for _, f := range files {
				ms.Responses = append(ms.Responses, fileResponse(dir+f.Name, f))
			}
		}
	} else {
		f, err := h.stats.Get(r.Context(), p.UserID, name)
		if errors.Is(err, statistics.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.internal(w, "propfind", err)
			return
		}
		ms.Responses = append(ms.Responses, fileResponse(href, f))
	}
	out, err := xml.Marshal(ms)
	if err != nil {
		h.internal(w, "propfind", err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, xml.Header)
	_, _ = w.Write(out)
}

func okProps(p prop) propstat {
	return propstat{Prop: p, Status: "HTTP/1.1 200 OK"}
}

func fileResponse(href string, f statistics.File) response {
	size := f.Size
	return response{Href: href, Propstat: okProps(prop{
		ContentLength: &size,
		LastModified:  f.ModifiedAt.UTC().Format(http.TimeFormat),
		ETag:          f.ETag,
	})}
}
