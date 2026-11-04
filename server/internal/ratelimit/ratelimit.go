// Package ratelimit ограничивает неудачные проверки ключа по адресу клиента:
// после MaxFails неудач за Window адрес блокируется на BlockFor.
//
// Перед медленной проверкой вызывается Acquire: он резервирует попытку, чтобы
// пачка параллельных запросов не проскочила проверку блока разом. Резерв
// закрывается Release (удача, бюджет не тратится) или Fail (неудача).
package ratelimit

import (
	"log/slog"
	"net/netip"
	"sync"
	"time"
)

const (
	MaxFails = 10
	Window   = 10 * time.Minute
	BlockFor = 15 * time.Minute

	// maxEntries ограничивает память: десятки байт на запись — единицы мегабайт.
	maxEntries = 100_000
	// purgeInterval — как часто полная таблица ищет истёкшие записи. Без паузы
	// флуд с новых адресов проходил бы всю таблицу под мьютексом на каждый запрос,
	// а записи всё равно истекают за минуты.
	purgeInterval = time.Minute
)

type entry struct {
	fails, inflight int
	windowStart     time.Time
	blockedUntil    time.Time
}

// Limiter — счётчики неудач по адресам. Безопасен для конкурентного использования.
type Limiter struct {
	log *slog.Logger
	now func() time.Time

	mu         sync.Mutex
	entries    map[netip.Prefix]*entry
	maxEntries int
	// overflow — общая запись для новых адресов, когда таблица заполнена
	// неистёкшими записями. Вытеснять старые нельзя: атакующий обнулял бы
	// свои счётчики, а пропускать без лимита — значит снять защиту флудом.
	overflow       entry
	overflowWarned bool
	nextPurge      time.Time
	purges         int // для тестов
}

// Option настраивает Limiter.
type Option func(*Limiter)

// WithClock подменяет источник времени.
func WithClock(now func() time.Time) Option { return func(l *Limiter) { l.now = now } }

// New создаёт ограничитель. В log пишется начало блокировки и переполнение таблицы.
func New(log *slog.Logger, opts ...Option) *Limiter {
	l := &Limiter{log: log, now: time.Now, entries: map[netip.Prefix]*entry{}, maxEntries: maxEntries}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Key — ключ счётчика: IPv4 — адрес, IPv6 — сеть /64 (абонент обычно получает
// /64 целиком и сменой адреса обходил бы лимит). IPv4-mapped приводится к IPv4.
func Key(a netip.Addr) netip.Prefix {
	a = a.Unmap().WithZone("")
	if !a.IsValid() {
		return netip.Prefix{}
	}
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p
	}
	return netip.PrefixFrom(a, 32)
}

// Attempt — зарезервированная попытка. Нулевое значение ни на что не влияет.
type Attempt struct {
	l   *Limiter
	e   *entry
	key netip.Prefix
}

// Acquire резервирует попытку для addr. При отказе возвращает, через сколько
// повторить: остаток блокировки или, если бюджет занят ещё не завершёнными
// попытками, полный срок блокировки — столько продлится блок, если они не удадутся.
func (l *Limiter) Acquire(addr netip.Addr) (Attempt, time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := Key(addr)
	e := l.lookup(key, now, true)
	l.expire(e, now)
	if now.Before(e.blockedUntil) {
		return Attempt{}, e.blockedUntil.Sub(now), false
	}
	if e.fails+e.inflight >= MaxFails {
		return Attempt{}, BlockFor, false
	}
	e.inflight++
	return Attempt{l: l, e: e, key: key}, 0, true
}

// Release закрывает удачную попытку: бюджет не тратится, блок не ставится.
func (a Attempt) Release() {
	if a.l == nil {
		return
	}
	l := a.l
	l.mu.Lock()
	defer l.mu.Unlock()
	a.e.inflight--
	// Запись без неудач не нужна: удачные входы не занимают таблицу.
	if a.e != &l.overflow && a.e.inflight == 0 && a.e.fails == 0 && a.e.blockedUntil.IsZero() {
		delete(l.entries, a.key)
	}
}

// Fail закрывает неудачную попытку; десятая неудача в окне ставит блок.
func (a Attempt) Fail() {
	if a.l == nil {
		return
	}
	l := a.l
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := a.e
	e.inflight--
	l.expire(e, now)
	if e.fails == 0 {
		e.windowStart = now
	}
	e.fails++
	if e.fails >= MaxFails && !now.Before(e.blockedUntil) {
		e.blockedUntil = now.Add(BlockFor)
		ip := "overflow"
		if e != &l.overflow {
			ip = keyString(a.key)
		}
		l.log.Warn("auth: client blocked", "ip", ip, "until", e.blockedUntil.UTC().Format(time.RFC3339), "fails", e.fails)
	}
}

// Blocked сообщает, заблокирован ли addr, и остаток блокировки.
func (l *Limiter) Blocked(addr netip.Addr) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.lookup(Key(addr), now, false)
	if e == nil || !now.Before(e.blockedUntil) {
		return false, 0
	}
	return true, e.blockedUntil.Sub(now)
}

// expire снимает истёкший блок и сбрасывает истёкшее окно.
func (l *Limiter) expire(e *entry, now time.Time) {
	if !e.blockedUntil.IsZero() && !now.Before(e.blockedUntil) {
		e.blockedUntil = time.Time{}
		e.fails = 0
	}
	if e.fails > 0 && !now.Before(e.windowStart.Add(Window)) {
		e.fails = 0
	}
}

func (l *Limiter) stale(e *entry, now time.Time) bool {
	l.expire(e, now)
	return e.inflight == 0 && e.fails == 0 && e.blockedUntil.IsZero()
}

// lookup находит запись адреса. Если её нет и create, создаёт; при заполненной
// таблице сначала удаляет истёкшие (не чаще purgeInterval), а если места нет — возвращает
// общую запись переполнения. Без create отсутствующий адрес даёт nil, кроме
// случая заполненной таблицы: такой адрес учитывался бы записью переполнения.
func (l *Limiter) lookup(key netip.Prefix, now time.Time, create bool) *entry {
	if e, ok := l.entries[key]; ok {
		return e
	}
	if !create {
		if len(l.entries) >= l.maxEntries {
			return &l.overflow
		}
		return nil
	}
	if len(l.entries) >= l.maxEntries && !now.Before(l.nextPurge) {
		l.nextPurge = now.Add(purgeInterval)
		l.purges++
		for k, e := range l.entries {
			if l.stale(e, now) {
				delete(l.entries, k)
			}
		}
	}
	if len(l.entries) >= l.maxEntries {
		if !l.overflowWarned {
			l.overflowWarned = true
			l.log.Warn("auth: rate limit table full, new clients share one counter", "entries", len(l.entries))
		}
		return &l.overflow
	}
	l.overflowWarned = false
	e := &entry{}
	l.entries[key] = e
	return e
}

func keyString(p netip.Prefix) string {
	if p.Addr().Is4() {
		return p.Addr().String()
	}
	return p.String()
}
