// Package clientip определяет адрес клиента с учётом доверенных reverse proxy.
//
// X-Forwarded-For читается, только если запрос пришёл от доверенного прокси, и
// разбирается справа налево: левые элементы подставляет сам клиент, а nginx
// ($proxy_add_x_forwarded_for) дописывает реальный адрес справа.
package clientip

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver определяет адрес клиента. Нулевой и nil Resolver доверенных прокси
// не знает и всегда возвращает адрес соединения.
type Resolver struct {
	trusted []netip.Prefix
}

// New создаёт Resolver со списком доверенных прокси.
func New(trusted []netip.Prefix) *Resolver {
	return &Resolver{trusted: trusted}
}

// Addr возвращает адрес клиента. Если адрес соединения не разбирается (например,
// unix-сокет), возвращается невалидный netip.Addr.
func (r *Resolver) Addr(req *http.Request) netip.Addr {
	conn := remoteAddr(req.RemoteAddr)
	if r == nil || !r.isTrusted(conn) {
		return conn
	}
	var hops []string
	for _, v := range req.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	if len(hops) == 0 {
		return conn
	}
	var leftmost netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return conn
		}
		a = normalize(a)
		if !r.isTrusted(a) {
			return a
		}
		leftmost = a
	}
	// Все элементы доверенные: клиент в той же сети, что и прокси.
	return leftmost
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func remoteAddr(s string) netip.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return normalize(ap.Addr())
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return normalize(a)
	}
	return netip.Addr{}
}

// normalize приводит IPv4-mapped адреса к IPv4 и убирает зону: иначе
// ::ffff:10.0.0.2 не попал бы в 10.0.0.0/8.
func normalize(a netip.Addr) netip.Addr {
	return a.Unmap().WithZone("")
}

// ParsePrefixes разбирает список через запятую: CIDR или одиночные адреса
// (сеть /32 или /128). Пустые элементы пропускаются.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		p, err := parsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("invalid address or CIDR %q", item)
		}
		out = append(out, p)
	}
	return out, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if !strings.Contains(s, "/") {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		a = normalize(a)
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
		p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
	}
	return p.Masked(), nil
}
