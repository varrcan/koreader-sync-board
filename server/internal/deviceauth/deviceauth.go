// Package deviceauth — вход HTTP Basic с паролем устройства для слоёв, которые
// обслуживают читалки по стандартным протоколам (WebDAV, OPDS).
package deviceauth

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/authlog"
	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
)

// Basic проверяет заголовок Authorization одного слоя.
type Basic struct {
	accounts *accounts.Service
	ip       *clientip.Resolver
	log      *slog.Logger
	layer    string
	// logMissing — писать ли в лог запрос без заголовка. Браузер и часть клиентов
	// сначала приходят без пароля, и такие заходы не должны идти в счёт fail2ban.
	logMissing bool
}

// New создаёт проверку для слоя layer: это имя попадает в строки лога.
func New(acc *accounts.Service, ip *clientip.Resolver, log *slog.Logger, layer string, logMissing bool) *Basic {
	return &Basic{accounts: acc, ip: ip, log: log, layer: layer, logMissing: logMissing}
}

// Authenticate проверяет Basic с паролем устройства. Пароль превращается в ключ
// kosync (md5), поэтому проверка, кеш и ограничитель — те же, что у kosync. При
// отказе ответ уже записан.
func (b *Basic) Authenticate(w http.ResponseWriter, r *http.Request) (accounts.Principal, bool) {
	client := b.ip.Addr(r)
	user, pass, ok := r.BasicAuth()
	if !ok {
		if b.logMissing {
			authlog.Failure(b.log, b.layer, r, client, "", "missing basic credentials")
		}
		unauthorized(w)
		return accounts.Principal{}, false
	}
	p, err := b.accounts.Authenticate(r.Context(), user, accounts.KeyFromPassword(pass), client)
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
			authlog.Failure(b.log, b.layer, r, client, user, err.Error())
		}
		unauthorized(w)
	default:
		b.log.Error(b.layer+": authenticate", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
	return accounts.Principal{}, false
}

// Reject отказывает уже прошедшему вход запросу: учётка перестала действовать по ходу работы.
func (b *Basic) Reject(w http.ResponseWriter, r *http.Request, user, reason string) {
	authlog.Failure(b.log, b.layer, r, b.ip.Addr(r), user, reason)
	unauthorized(w)
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="koreader-sync-board"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
