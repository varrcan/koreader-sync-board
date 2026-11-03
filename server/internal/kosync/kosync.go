// Package kosync реализует протокол синхронизации прогресса KOReader, совместимый
// с koreader/koreader-sync-server.
//
// Обработчик повторяет фреймворк эталона ostinelli/gin в том же порядке проверок:
// тело (gin/core/request.lua) → Accept → маршрут (gin/core/router.lua) → контроллер
// (app/controllers/1/syncs_controller.lua). Порядок определяет, какой код ошибки
// получит клиент.
package kosync

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
)

// Options — переключатели поведения протокола.
type Options struct {
	// Registration включает POST /users/create (иначе 2005).
	Registration bool
	// SelfDelete включает DELETE /users/me (иначе 2003).
	SelfDelete bool
}

// Handler обслуживает /users/*, /syncs/* и /healthcheck.
type Handler struct {
	accounts *accounts.Service
	progress *progress.Store
	opts     Options
	log      *slog.Logger
	now      func() time.Time

	// beforeWrite вызывается между проверкой ключа и записью прогресса (для тестов).
	beforeWrite func()
}

// New создаёт обработчик.
func New(acc *accounts.Service, prog *progress.Store, opts Options, log *slog.Logger) *Handler {
	return &Handler{accounts: acc, progress: prog, opts: opts, log: log, now: time.Now}
}

// maxBody ограничивает тело запроса: запросы KOReader занимают сотни байт.
const maxBody = 1 << 20

// acceptRe — шаблон gin "^application/vnd." .. name .. ".v(%d+)(.*)+json$".
// Точки в Lua-шаблоне — любой символ, «+» после «)» — литерал.
var acceptRe = regexp.MustCompile(`(?s)^application/vnd.koreader.v(\d+)(.*)\+json$`)

type action func(h *Handler, c *call) (int, any)

type route struct {
	method  string
	pattern *regexp.Regexp
	action  action
}

// routePattern повторяет Version:add из gin/core/routes.lua: ":param" превращается
// в "([A-Za-z0-9_]+)", а к шаблону дописывается "/???$" — необязательный «/» и
// необязательный литеральный «?».
func routePattern(p string) *regexp.Regexp {
	p = regexp.MustCompile(`/:[A-Za-z0-9_]+`).ReplaceAllString(p, `/([A-Za-z0-9_]+)`)
	return regexp.MustCompile(`^` + p + `/?\??$`)
}

// routes — config/routes.lua эталона в том же порядке.
var routes = []route{
	{http.MethodPost, routePattern("/users/create"), (*Handler).createUser},
	{http.MethodGet, routePattern("/users/auth"), (*Handler).authUser},
	{http.MethodDelete, routePattern("/users/me"), (*Handler).deleteUser},
	{http.MethodPut, routePattern("/users/password"), (*Handler).updatePassword},
	{http.MethodPut, routePattern("/syncs/progress"), (*Handler).updateProgress},
	{http.MethodGet, routePattern("/syncs/progress/:document"), (*Handler).getProgress},
	{http.MethodGet, routePattern("/healthcheck"), (*Handler).healthcheck},
}

// call — разобранный запрос.
type call struct {
	r      *http.Request
	body   map[string]any
	params []string
}

// apiError — ошибка с кодом из config/errors.lua эталона или gin/core/error.lua.
type apiError struct {
	status  int
	code    int
	message string
}

var (
	errAcceptMissing = apiError{412, 100, "Accept header not set."}
	errAcceptFormat  = apiError{412, 101, "Invalid Accept header format."}
	errAcceptVersion = apiError{412, 102, "Unsupported version specified in the Accept header."}
	errBadJSON       = apiError{400, 103, "Could not parse JSON in body."}
	errNotHash       = apiError{400, 104, "Body should be a JSON hash."}

	errInternal       = apiError{502, 2000, "Unknown server error."}
	errUnauthorized   = apiError{401, 2001, "Unauthorized"}
	errUserExists     = apiError{402, 2002, "Username is already registered."}
	errInvalidFields  = apiError{403, 2003, "Invalid request"}
	errNoDocument     = apiError{403, 2004, "Field 'document' not provided."}
	errRegDisabled    = apiError{402, 2005, "User registration is disabled."}
	errNoAccount      = apiError{404, 2006, "Account not found."}
	errNotServableDoc = apiError{403, 2007, "Field 'document' contains characters the read route cannot serve."}
)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Тело разбирается для любого метода, раньше проверки Accept.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		return
	}
	body, perr := parseBody(raw)
	if perr != nil {
		writeError(w, *perr)
		return
	}

	// 2. Accept.
	accepts := r.Header.Values("Accept")
	if len(accepts) == 0 {
		writeError(w, errAcceptMissing)
		return
	}
	m := acceptRe.FindStringSubmatch(accepts[0])
	if m == nil {
		writeError(w, errAcceptFormat)
		return
	}
	if v, err := strconv.Atoi(m[1]); err != nil || v != 1 {
		writeError(w, errAcceptVersion)
		return
	}

	// 3. Маршрут: нет совпадения по методу и пути — 404 ещё до контроллера.
	for _, rt := range routes {
		if rt.method != r.Method {
			continue
		}
		if pm := rt.pattern.FindStringSubmatch(r.URL.Path); pm != nil {
			status, resp := rt.action(h, &call{r: r, body: body, params: pm[1:]})
			if e, ok := resp.(apiError); ok {
				writeError(w, e)
				return
			}
			writeJSON(w, status, resp)
			return
		}
	}
	http.NotFound(w, r)
}

// parseBody повторяет Request.new из gin/core/request.lua. Непустой JSON-массив — 104.
// Пустое тело, null, скаляры и [] у эталона приводят к 500 или к случайному
// поведению; здесь они считаются пустым объектом, и запрос идёт в валидацию контроллера.
func parseBody(raw []byte) (map[string]any, *apiError) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, &errBadJSON
	}
	switch v := v.(type) {
	case map[string]any:
		return v, nil
	case []any:
		if len(v) > 0 {
			return nil, &errNotHash
		}
	}
	return map[string]any{}, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e apiError) {
	writeJSON(w, e.status, map[string]any{"code": e.code, "message": e.message})
}

// isValidField — непустая строка (is_valid_field).
func isValidField(v any) bool {
	s, ok := v.(string)
	return ok && s != ""
}

// isValidKeyField — непустая строка без «:» (is_valid_key_field).
func isValidKeyField(v any) bool {
	return isValidField(v) && !strings.Contains(v.(string), ":")
}

var servableRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// header возвращает заголовок, только если он передан ровно один раз: у эталона
// повторённый заголовок становится таблицей и не проходит проверку на строку.
func header(r *http.Request, name string) any {
	vs := r.Header.Values(name)
	if len(vs) != 1 {
		return nil
	}
	return vs[0]
}

// credentials возвращает x-auth-user и x-auth-key, если они проходят проверки формата.
func credentials(r *http.Request) (user, key string, ok bool) {
	u, k := header(r, "x-auth-user"), header(r, "x-auth-key")
	if !isValidField(k) || !isValidKeyField(u) {
		return "", "", false
	}
	return u.(string), k.(string), true
}

// authorize — SyncsController:authorize.
func (h *Handler) authorize(c *call) (accounts.Principal, *apiError) {
	user, key, ok := credentials(c.r)
	if !ok {
		h.logFailure(c.r, user, "invalid credentials headers")
		return accounts.Principal{}, &errUnauthorized
	}
	return h.authenticate(c.r, user, key)
}

func (h *Handler) authenticate(r *http.Request, user, key string) (accounts.Principal, *apiError) {
	p, err := h.accounts.Authenticate(r.Context(), user, key)
	if errors.Is(err, accounts.ErrUnauthorized) {
		h.logFailure(r, user, err.Error())
		return accounts.Principal{}, &errUnauthorized
	}
	if err != nil {
		h.log.Error("kosync: authenticate", "err", err)
		return accounts.Principal{}, &errInternal
	}
	return p, nil
}

// logFailure пишет неудачную попытку входа: имя и адрес, без ключа.
func (h *Handler) logFailure(r *http.Request, user, reason string) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if user == "" {
		if v, ok := header(r, "x-auth-user").(string); ok {
			user = v
		}
	}
	h.log.Warn("kosync: authorization failed", "user", user, "ip", ip, "path", r.URL.Path, "reason", reason)
}

func (h *Handler) authUser(c *call) (int, any) {
	if _, e := h.authorize(c); e != nil {
		return 0, *e
	}
	return http.StatusOK, map[string]any{"authorized": "OK"}
}

func (h *Handler) createUser(c *call) (int, any) {
	if !h.opts.Registration {
		return 0, errRegDisabled
	}
	username, password := c.body["username"], c.body["password"]
	if !isValidKeyField(username) || !isValidField(password) {
		return 0, errInvalidFields
	}
	err := h.accounts.CreateUserWithDevice(c.r.Context(), username.(string), "KOReader", password.(string))
	if errors.Is(err, accounts.ErrUserExists) {
		return 0, errUserExists
	}
	if err != nil {
		h.log.Error("kosync: create user", "err", err)
		return 0, errInternal
	}
	h.log.Info("kosync: user registered", "user", username)
	return http.StatusCreated, map[string]any{"username": username}
}

// deleteUser — по умолчанию выключен: с читалки легко удалить аккаунт со всеми
// устройствами. С SelfDelete ведёт себя как эталон: 2001 при неверном ключе,
// 2006 при отсутствии пользователя.
func (h *Handler) deleteUser(c *call) (int, any) {
	if !h.opts.SelfDelete {
		return 0, errInvalidFields
	}
	user, key, ok := credentials(c.r)
	if !ok {
		h.logFailure(c.r, user, "invalid credentials headers")
		return 0, errUnauthorized
	}
	p, err := h.accounts.Authenticate(c.r.Context(), user, key)
	switch {
	case errors.Is(err, accounts.ErrUnknownUser):
		return 0, errNoAccount
	case errors.Is(err, accounts.ErrUnauthorized):
		h.logFailure(c.r, user, err.Error())
		return 0, errUnauthorized
	case err != nil:
		h.log.Error("kosync: authenticate", "err", err)
		return 0, errInternal
	}
	if err := h.accounts.DeleteUserByID(c.r.Context(), p.UserID); err != nil && !errors.Is(err, accounts.ErrNotFound) {
		h.log.Error("kosync: delete user", "err", err)
		return 0, errInternal
	}
	h.log.Info("kosync: user deleted itself", "user", user)
	return http.StatusOK, map[string]any{"deleted": true}
}

// updatePassword меняет ключ только того устройства, которым выполнен вход.
// Порядок как у эталона: заголовки → тело → ключ.
func (h *Handler) updatePassword(c *call) (int, any) {
	user, key, ok := credentials(c.r)
	if !ok {
		h.logFailure(c.r, user, "invalid credentials headers")
		return 0, errUnauthorized
	}
	newKey := c.body["password"]
	if !isValidField(newKey) {
		return 0, errInvalidFields
	}
	p, e := h.authenticate(c.r, user, key)
	if e != nil {
		return 0, *e
	}
	err := h.accounts.RotateKey(c.r.Context(), p, newKey.(string))
	if errors.Is(err, accounts.ErrUnauthorized) {
		return 0, errUnauthorized
	}
	if err != nil {
		h.log.Error("kosync: rotate key", "err", err)
		return 0, errInternal
	}
	return http.StatusOK, map[string]any{"updated": true}
}

func (h *Handler) getProgress(c *call) (int, any) {
	p, e := h.authorize(c)
	if e != nil {
		return 0, *e
	}
	doc := c.params[0]
	if !isValidKeyField(doc) {
		return 0, errNoDocument
	}
	cur, err := h.progress.Get(c.r.Context(), p.UserID, doc)
	if errors.Is(err, progress.ErrNoProgress) {
		return http.StatusOK, map[string]any{}
	}
	if err != nil {
		h.log.Error("kosync: get progress", "err", err)
		return 0, errInternal
	}
	res := map[string]any{
		"document":   doc,
		"percentage": cur.Percentage,
		"progress":   cur.Progress,
		"device":     cur.Device,
		"timestamp":  cur.Timestamp,
	}
	if cur.DeviceID != nil {
		res["device_id"] = *cur.DeviceID
	}
	return http.StatusOK, res
}

// updateProgress — порядок проверок как в SyncsController:update_progress.
func (h *Handler) updateProgress(c *call) (int, any) {
	p, e := h.authorize(c)
	if e != nil {
		return 0, *e
	}
	doc := c.body["document"]
	if !isValidKeyField(doc) {
		return 0, errNoDocument
	}
	if !servableRe.MatchString(doc.(string)) {
		return 0, errNotServableDoc
	}
	percentage, okPct := luaToNumber(c.body["percentage"])
	prog, okProg := luaToString(c.body["progress"])
	device, okDev := luaToString(c.body["device"])
	if !okPct || !okProg || !okDev {
		return 0, errInvalidFields
	}
	u := progress.Update{
		Document: doc.(string),
		// Эталон пишет число в Redis через tostring (%.14g) и читает обратно tonumber.
		Percentage: roundG14(percentage),
		Progress:   prog,
		Device:     device,
		ReceivedAt: h.now(),
	}
	if id, ok := luaToString(c.body["device_id"]); ok {
		u.DeviceID = &id
	}
	u.MetaJSON, u.Meta = parseMetadata(c.body["metadata"])

	if h.beforeWrite != nil {
		h.beforeWrite()
	}
	err := h.progress.Save(c.r.Context(), p, u)
	if errors.Is(err, accounts.ErrUnauthorized) {
		h.logFailure(c.r, p.Username, "credential changed before write")
		return 0, errUnauthorized
	}
	if err != nil {
		h.log.Error("kosync: save progress", "err", err)
		return 0, errInternal
	}
	return http.StatusOK, map[string]any{"document": u.Document, "timestamp": u.ReceivedAt.Unix()}
}

func (h *Handler) healthcheck(*call) (int, any) {
	return http.StatusOK, map[string]any{"state": "OK"}
}

// parseMetadata принимает metadata, только если это объект. Поля filename, title,
// authors приводятся к строкам, остальное игнорируется.
func parseMetadata(v any) ([]byte, progress.Metadata) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, progress.Metadata{}
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, progress.Metadata{}
	}
	field := func(k string) string {
		s, _ := luaToString(obj[k])
		return s
	}
	return raw, progress.Metadata{Filename: field("filename"), Title: field("title"), Authors: field("authors")}
}

// luaToString — значение, которое эталон положил бы в Redis: строка как есть,
// число через tostring (%.14g), true как "true". null и false — «нет значения».
func luaToString(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'g', 14, 64), true
	case bool:
		return "true", v
	}
	return "", false
}

// luaToNumber — Lua tonumber для числа или строки.
func luaToNumber(v any) (float64, bool) {
	switch v := v.(type) {
	case float64:
		return v, true
	case string:
		s := strings.Trim(v, " \t\n\v\f\r")
		if s == "" {
			return 0, false
		}
		neg := false
		body := s
		if body[0] == '-' || body[0] == '+' {
			neg = body[0] == '-'
			body = body[1:]
		}
		if len(body) > 2 && body[0] == '0' && (body[1] == 'x' || body[1] == 'X') {
			n, err := strconv.ParseUint(body[2:], 16, 64)
			if err != nil {
				return 0, false
			}
			f := float64(n)
			if neg {
				f = -f
			}
			return f, true
		}
		lower := strings.ToLower(body)
		if strings.Contains(lower, "inf") || strings.Contains(lower, "nan") || strings.Contains(body, "_") {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func roundG14(f float64) float64 {
	r, err := strconv.ParseFloat(strconv.FormatFloat(f, 'g', 14, 64), 64)
	if err != nil {
		return f
	}
	return r
}
