// Package panelauth — HTTP-часть входа в панель: cookie сессии, middleware,
// проверяющий сессию у операций API, код мастера первого запуска и лог неудач.
// Данные (пароли, сессии, пользователи) живут в accounts.
package panelauth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/authlog"
	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
)

// CookieName — имя cookie сессии.
const CookieName = "ksb_session"

// publicPatterns — операции, доступные без сессии; ключи — r.Pattern из
// сгенерированного маршрутизатора. Всё остальное требует сессию, так что новый
// эндпоинт защищён по умолчанию.
var publicPatterns = map[string]bool{
	"GET /api/v1/health":       true,
	"GET /api/v1/version":      true,
	"POST /api/v1/auth/login":  true,
	"POST /api/v1/auth/logout": true,
	"GET /api/v1/setup":        true,
	"POST /api/v1/setup":       true,
}

// Auth проверяет сессии панели.
type Auth struct {
	acc *accounts.Service
	ip  *clientip.Resolver
	log *slog.Logger
}

// New создаёт Auth. ip определяет адрес клиента для ограничителя и лога.
func New(acc *accounts.Service, ip *clientip.Resolver, log *slog.Logger) *Auth {
	return &Auth{acc: acc, ip: ip, log: log}
}

// Current — владелец сессии запроса.
type Current struct {
	Session accounts.Session
	User    accounts.User
}

type ctxKey struct{}

// FromContext возвращает владельца сессии, если запрос прошёл через Middleware
// на защищённой операции.
func FromContext(ctx context.Context) (Current, bool) {
	c, ok := ctx.Value(ctxKey{}).(Current)
	return c, ok
}

// Middleware требует действующую сессию у всех операций, кроме публичных. Его
// подключают через StdHTTPServerOptions.Middlewares: сгенерированный код
// оборачивает им только известные операции, и r.Pattern уже заполнен.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPatterns[r.Pattern] {
			next.ServeHTTP(w, r)
			return
		}
		cur, err := a.Session(r)
		if errors.Is(err, accounts.ErrUnauthorized) {
			WriteError(w, http.StatusUnauthorized, "unauthorized", "session required")
			return
		}
		if err != nil {
			a.log.Error("panel: session lookup", "err", err)
			WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
			return
		}
		if cur.Session.Renewed {
			if c, err := r.Cookie(CookieName); err == nil {
				SetCookie(w, r, c.Value)
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, cur)))
	})
}

// Session находит сессию по cookie запроса; без cookie — accounts.ErrUnauthorized.
func (a *Auth) Session(r *http.Request) (Current, error) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Current{}, accounts.ErrUnauthorized
	}
	s, u, err := a.acc.SessionByToken(r.Context(), c.Value)
	if err != nil {
		return Current{}, err
	}
	return Current{Session: s, User: u}, nil
}

// ClientAddr — адрес клиента с учётом доверенных прокси.
func (a *Auth) ClientAddr(r *http.Request) netip.Addr {
	return a.ip.Addr(r)
}

// LogFailure пишет неудачную проверку пароля или кода настройки. Порядок полей
// ip → path тот же, что у kosync: по нему работает фильтр fail2ban из README.
func (a *Auth) LogFailure(r *http.Request, user, reason string) {
	authlog.Failure(a.log, "panel", r, a.ClientAddr(r), user, reason)
}

// SetCookie выставляет cookie сессии на SessionTTL: его ставят при создании и
// продлении сессии, когда срок в базе тоже равен SessionTTL. Secure ставится только при
// HTTPS, иначе панель не работала бы по http:// в локальной сети. Заголовку
// X-Forwarded-Proto верим без проверки прокси: подделав его, клиент лишь
// добавит Secure своему же cookie.
func SetCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(accounts.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie стирает cookie сессии.
func ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// CrossOrigin оборачивает h защитой от CSRF: изменяющие запросы браузера с
// чужого origin получают JSON 403 cross_origin. Запросы без Sec-Fetch-Site и
// Origin (curl, скрипты) проходят дальше к обычной проверке.
func CrossOrigin(h http.Handler) http.Handler {
	p := http.NewCrossOriginProtection()
	p.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusForbidden, "cross_origin", "cross-origin request rejected")
	}))
	return p.Handler(h)
}

// WriteError пишет ошибку API в формате {code, message}.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// WriteRateLimited — 429 too_many_attempts с Retry-After в секундах.
func WriteRateLimited(w http.ResponseWriter, retry time.Duration) {
	w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(retry.Seconds())), 10))
	WriteError(w, http.StatusTooManyRequests, "too_many_attempts", "too many failed attempts")
}
