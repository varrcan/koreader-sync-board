package ratelimit

import (
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newLimiter(t *testing.T) (*Limiter, *clock, *strings.Builder) {
	t.Helper()
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	var buf strings.Builder
	return New(slog.New(slog.NewJSONHandler(&buf, nil)), WithClock(c.now)), c, &buf
}

var addrA = netip.MustParseAddr("198.51.100.7")

func fail(t *testing.T, l *Limiter, a netip.Addr, n int) {
	t.Helper()
	for i := range n {
		att, _, ok := l.Acquire(a)
		if !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
		att.Fail()
	}
}

func TestBlockAfterMaxFails(t *testing.T) {
	l, c, log := newLimiter(t)
	fail(t, l, addrA, MaxFails)
	if _, retry, ok := l.Acquire(addrA); ok || retry != BlockFor {
		t.Fatalf("after %d fails: ok=%v retry=%v", MaxFails, ok, retry)
	}
	if strings.Count(log.String(), "auth: client blocked") != 1 || !strings.Contains(log.String(), `"ip":"198.51.100.7"`) {
		t.Fatalf("log: %s", log)
	}
	if _, _, ok := l.Acquire(netip.MustParseAddr("198.51.100.8")); !ok {
		t.Fatal("other address blocked")
	}

	c.advance(5 * time.Minute)
	blocked, left := l.Blocked(addrA)
	if !blocked || left != 10*time.Minute {
		t.Fatalf("Blocked = %v %v", blocked, left)
	}
	if _, retry, ok := l.Acquire(addrA); ok || retry != 10*time.Minute {
		t.Fatalf("retry = %v", retry)
	}

	c.advance(10 * time.Minute)
	if blocked, _ := l.Blocked(addrA); blocked {
		t.Fatal("block not lifted")
	}
	// Блок снят, счётчик начинается заново.
	fail(t, l, addrA, MaxFails-1)
	if _, _, ok := l.Acquire(addrA); !ok {
		t.Fatal("fails not reset after block")
	}
}

func TestWindowExpires(t *testing.T) {
	l, c, _ := newLimiter(t)
	fail(t, l, addrA, MaxFails-1)
	c.advance(Window)
	fail(t, l, addrA, 1)
	if blocked, _ := l.Blocked(addrA); blocked {
		t.Fatal("blocked across windows")
	}
	fail(t, l, addrA, MaxFails-2)
	if _, _, ok := l.Acquire(addrA); !ok {
		t.Fatal("new window counts old fails")
	}
}

func TestSuccessDoesNotBlock(t *testing.T) {
	l, _, _ := newLimiter(t)
	fail(t, l, addrA, MaxFails-1)
	for range 5 {
		att, _, ok := l.Acquire(addrA)
		if !ok {
			t.Fatal("refused")
		}
		att.Release()
	}
	if blocked, _ := l.Blocked(addrA); blocked {
		t.Fatal("success blocked")
	}
	// Удачная попытка с чистого адреса не оставляет записи.
	b := netip.MustParseAddr("203.0.113.1")
	att, _, _ := l.Acquire(b)
	att.Release()
	if _, ok := l.entries[Key(b)]; ok {
		t.Fatal("entry kept after success")
	}
	var zero Attempt
	zero.Release()
	zero.Fail()
}

func TestBurst(t *testing.T) {
	for _, prior := range []int{0, 4} {
		l, _, _ := newLimiter(t)
		fail(t, l, addrA, prior)
		var passed atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		atts := make(chan Attempt, 100)
		for range 100 {
			wg.Go(func() {
				<-start
				if att, _, ok := l.Acquire(addrA); ok {
					passed.Add(1)
					atts <- att
				}
			})
		}
		close(start)
		wg.Wait()
		close(atts)
		if got := passed.Load(); got != int64(MaxFails-prior) {
			t.Fatalf("prior=%d: %d passed, want %d", prior, got, MaxFails-prior)
		}
		for att := range atts {
			att.Fail()
		}
		if blocked, _ := l.Blocked(addrA); !blocked {
			t.Fatal("burst did not block")
		}
	}
}

func TestIPv6Prefix(t *testing.T) {
	l, _, log := newLimiter(t)
	for i := range MaxFails {
		a := netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 1, 0, 2, 0, 0, 0, 0, 0, 0, 0, byte(i + 1)})
		fail(t, l, a, 1)
	}
	if blocked, _ := l.Blocked(netip.MustParseAddr("2001:db8:1:2:ffff::1")); !blocked {
		t.Fatal("/64 not aggregated")
	}
	if blocked, _ := l.Blocked(netip.MustParseAddr("2001:db8:1:3::1")); blocked {
		t.Fatal("neighbour /64 blocked")
	}
	if !strings.Contains(log.String(), `"ip":"2001:db8:1:2::/64"`) {
		t.Fatalf("log: %s", log)
	}
	// IPv4-mapped — тот же счётчик, что у IPv4, а не общая /64.
	fail(t, l, netip.MustParseAddr("::ffff:198.51.100.7"), MaxFails)
	if blocked, _ := l.Blocked(addrA); !blocked {
		t.Fatal("mapped address counted separately")
	}
	if blocked, _ := l.Blocked(netip.MustParseAddr("::ffff:198.51.100.8")); blocked {
		t.Fatal("all mapped addresses share a counter")
	}
}

func TestOverflow(t *testing.T) {
	l, c, log := newLimiter(t)
	addr := func(i int) netip.Addr {
		return netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
	}
	// Половина записей истечёт, половина — нет.
	for i := range maxEntries / 2 {
		fail(t, l, addr(i), 1)
	}
	c.advance(Window)
	tracked := addr(maxEntries / 2)
	fail(t, l, tracked, MaxFails-1)
	for i := maxEntries/2 + 1; i < maxEntries; i++ {
		fail(t, l, addr(i), 1)
	}
	if len(l.entries) != maxEntries {
		t.Fatalf("entries = %d", len(l.entries))
	}

	// Полная таблица: истёкшие записи вычищаются, новому адресу находится место.
	fail(t, l, addr(maxEntries), 1)
	if len(l.entries) != maxEntries/2+1 || strings.Contains(log.String(), "table full") {
		t.Fatalf("expired not purged: %d entries", len(l.entries))
	}
	for i := maxEntries + 1; len(l.entries) < maxEntries; i++ {
		fail(t, l, addr(i), 1)
	}

	// Места нет: новые адреса делят общую запись переполнения.
	n1, n2 := netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("203.0.113.2")
	fail(t, l, n1, MaxFails/2)
	fail(t, l, n2, MaxFails/2)
	if _, _, ok := l.Acquire(netip.MustParseAddr("203.0.113.3")); ok {
		t.Fatal("overflow entry not shared")
	}
	if strings.Count(log.String(), "table full") != 1 {
		t.Fatalf("overflow warnings: %s", log)
	}
	// Полная таблица не перебирается на каждый новый адрес, только раз в purgeInterval.
	purges := l.purges
	for i := range 100 {
		l.Acquire(netip.AddrFrom4([4]byte{203, 0, 114, byte(i)}))
	}
	if l.purges != purges {
		t.Fatalf("%d purges within purgeInterval", l.purges-purges)
	}
	c.advance(purgeInterval)
	l.Acquire(netip.MustParseAddr("203.0.115.1"))
	if l.purges != purges+1 {
		t.Fatal("no purge after purgeInterval")
	}
	// Отслеживаемый адрес считается своей записью.
	if _, _, ok := l.Acquire(tracked); !ok {
		t.Fatal("tracked address affected by overflow")
	}
}
