// Package authlog пишет неудачные попытки входа одной строкой для всех слоёв:
// по ней работает фильтр fail2ban из docs/security.md, поэтому формат и порядок полей
// (user → ip → path → reason) не должны расходиться между kosync, WebDAV и панелью.
package authlog

import (
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
)

// maxUser — сколько байт имени пользователя попадает в лог.
const maxUser = 64

// Failure пишет строку "<layer>: authorization failed". client — адрес клиента с
// учётом доверенных прокси; без него берётся адрес соединения. Ключ и пароль в
// лог не попадают.
func Failure(log *slog.Logger, layer string, r *http.Request, client netip.Addr, user, reason string) {
	ip := r.RemoteAddr
	if client.IsValid() {
		ip = client.String()
	}
	// Имя приходит из заголовка и может занимать до 1 MiB, а отказы без проверки
	// ключа ограничителем не считаются: без обрезки лог раздувается.
	if len(user) > maxUser {
		user = strings.ToValidUTF8(user[:maxUser], "") + "…"
	}
	log.Warn(layer+": authorization failed", "user", user, "ip", ip, "path", r.URL.Path, "reason", reason)
}
