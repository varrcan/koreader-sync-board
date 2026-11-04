package accounts

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

var client = netip.MustParseAddr("192.0.2.1")

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type env struct {
	ctx   context.Context
	path  string
	db    *store.DB
	svc   *Service
	clock *clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), store.FileName)
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	return &env{ctx: ctx, path: path, db: db, clock: c, svc: New(db, WithClock(c.now), WithArgon2Params(TestArgon2Params))}
}

// cli открывает базу отдельным соединением, как это делает `ksb user|device ...`.
func (e *env) cli(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(e.ctx, e.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, WithClock(e.clock.now), WithArgon2Params(TestArgon2Params))
}

func (e *env) device(t *testing.T, user, name string) string {
	t.Helper()
	pass, err := e.svc.AddDevice(e.ctx, user, name)
	if err != nil {
		t.Fatal(err)
	}
	return md5hex(pass)
}

func mustUser(t *testing.T, e *env, name string, role Role) {
	t.Helper()
	if _, err := e.svc.CreateUser(e.ctx, name, role); err != nil {
		t.Fatal(err)
	}
}

func TestCreateUser(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleAdmin)
	mustUser(t, e, "bob", RoleUser)
	if _, err := e.svc.CreateUser(e.ctx, "alice", RoleUser); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, bad := range []string{"", "a:b"} {
		if _, err := e.svc.CreateUser(e.ctx, bad, RoleUser); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if _, err := e.svc.CreateUser(e.ctx, "carol", Role("root")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad role: %v", err)
	}
	users, err := e.svc.ListUsers(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].Username != "alice" || users[0].Role != RoleAdmin || users[1].Role != RoleUser {
		t.Fatalf("users = %+v", users)
	}
}

func TestDevicesAuthenticate(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	kindle := e.device(t, "alice", "Kindle")
	kobo := e.device(t, "alice", "Kobo")
	if kindle == kobo {
		t.Fatal("same password for two devices")
	}
	if _, err := e.svc.AddDevice(e.ctx, "alice", "Kindle"); !errors.Is(err, ErrDeviceExists) {
		t.Fatalf("duplicate device: %v", err)
	}
	if _, err := e.svc.AddDevice(e.ctx, "nobody", "Kindle"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("device for unknown user: %v", err)
	}

	p1, err := e.svc.Authenticate(e.ctx, "alice", kindle, client)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := e.svc.Authenticate(e.ctx, "alice", kobo, client)
	if err != nil {
		t.Fatal(err)
	}
	if p1.UserID != p2.UserID || p1.CredentialID == p2.CredentialID {
		t.Fatalf("principals %+v %+v", p1, p2)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", md5hex("wrong"), client); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "ghost", kindle, client); !errors.Is(err, ErrUnknownUser) || !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown user: %v", err)
	}

	devs, err := e.svc.ListDevices(e.ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || devs[0].Name != "Kindle" || devs[0].LastUsedAt == nil || devs[0].RevokedAt != nil {
		t.Fatalf("devices = %+v", devs)
	}
}

func TestRevokeAndDisableFromOtherConnection(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	kindle := e.device(t, "alice", "Kindle")
	kobo := e.device(t, "alice", "Kobo")
	// Прогреваем кеш сервера.
	for _, k := range []string{kindle, kobo} {
		if _, err := e.svc.Authenticate(e.ctx, "alice", k, client); err != nil {
			t.Fatal(err)
		}
	}
	before := e.svc.argonCalls.Load()
	if _, err := e.svc.Authenticate(e.ctx, "alice", kindle, client); err != nil {
		t.Fatal(err)
	}
	if e.svc.argonCalls.Load() != before {
		t.Fatal("cached check ran argon2")
	}

	cli := e.cli(t)
	if err := cli.RevokeDevice(e.ctx, "alice", "Kindle"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kindle, client); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked device still authorized: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kobo, client); err != nil {
		t.Fatalf("other device broken: %v", err)
	}

	if err := cli.DisableUser(e.ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kobo, client); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled user authorized: %v", err)
	}
}

func TestRotateKey(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	kindle := e.device(t, "alice", "Kindle")
	kobo := e.device(t, "alice", "Kobo")
	p, err := e.svc.Authenticate(e.ctx, "alice", kindle, client)
	if err != nil {
		t.Fatal(err)
	}
	// Смена ключа другим экземпляром сервиса: кеш первого о ней не знает.
	cli := e.cli(t)
	newKey := md5hex("new password")
	if err := cli.RotateKey(e.ctx, p, newKey); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kindle, client); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old cached key works: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", newKey, client); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kobo, client); err != nil {
		t.Fatal(err)
	}
	// Повторная ротация по устаревшему принципалу отклоняется.
	if err := e.svc.RotateKey(e.ctx, p, md5hex("x")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale rotate: %v", err)
	}
}

func TestCheckTx(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	key := e.device(t, "alice", "Kindle")
	p, err := e.svc.Authenticate(e.ctx, "alice", key, client)
	if err != nil {
		t.Fatal(err)
	}
	check := func() error {
		tx, err := e.db.Write.BeginTx(e.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return CheckTx(e.ctx, tx, p)
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeDevice(e.ctx, "alice", "Kindle"); err != nil {
		t.Fatal(err)
	}
	if err := check(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("CheckTx after revoke: %v", err)
	}
}

func TestDeleteUser(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	key := e.device(t, "alice", "Kindle")
	if err := e.svc.DeleteUser(e.ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", key, client); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("deleted user: %v", err)
	}
	var n int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM device_credentials`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d devices left", n)
	}
	if err := e.svc.DeleteUser(e.ctx, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDummyCheckForUnknownUser(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	e.device(t, "alice", "Kindle")
	cases := []struct{ user string }{{"ghost"}, {"alice"}}
	for _, c := range cases {
		before := e.svc.argonCalls.Load()
		_, _ = e.svc.Authenticate(e.ctx, c.user, md5hex("wrong"), client)
		if got := e.svc.argonCalls.Load() - before; got != 1 {
			t.Fatalf("%s: %d argon2 calls, want 1", c.user, got)
		}
	}
	// Пользователь без устройств: тоже одна фиктивная проверка.
	mustUser(t, e, "bob", RoleUser)
	before := e.svc.argonCalls.Load()
	_, _ = e.svc.Authenticate(e.ctx, "bob", md5hex("wrong"), client)
	if got := e.svc.argonCalls.Load() - before; got != 1 {
		t.Fatalf("bob: %d argon2 calls, want 1", got)
	}
}

func TestNoPlaintextKeys(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	pass, err := e.svc.AddDevice(e.ctx, "alice", "Kindle")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CreateUserWithDevice(e.ctx, "bob", "KOReader", md5hex("bobpass"), client); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Bootstrap(e.ctx, "root", "rootpass"); err != nil {
		t.Fatal(err)
	}
	needles := []string{pass, md5hex(pass), "bobpass", md5hex("bobpass"), "rootpass", md5hex("rootpass")}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(e.path + suffix)
		if err != nil {
			continue
		}
		for _, n := range needles {
			if bytes.Contains(data, []byte(n)) {
				t.Fatalf("%q found in %s", n, filepath.Base(e.path+suffix))
			}
		}
	}
}

func TestLastUsedAtPerMinute(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	key := e.device(t, "alice", "Kindle")
	lastUsed := func() int64 {
		var v int64
		if err := e.db.Read.QueryRow(`SELECT last_used_at FROM device_credentials`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	t0 := e.clock.t
	if _, err := e.svc.Authenticate(e.ctx, "alice", key, client); err != nil {
		t.Fatal(err)
	}
	if lastUsed() != t0.Unix() {
		t.Fatal("first use not recorded")
	}
	e.clock.t = t0.Add(30 * time.Second)
	writes := e.svc.lastUsedWrites.Load()
	if _, err := e.svc.Authenticate(e.ctx, "alice", key, client); err != nil {
		t.Fatal(err)
	}
	if lastUsed() != t0.Unix() || e.svc.lastUsedWrites.Load() != writes {
		t.Fatal("last_used_at rewritten within a minute")
	}
	e.clock.t = t0.Add(61 * time.Second)
	if _, err := e.svc.Authenticate(e.ctx, "alice", key, client); err != nil {
		t.Fatal(err)
	}
	if lastUsed() != e.clock.t.Unix() {
		t.Fatal("last_used_at not updated after a minute")
	}
}

func TestBootstrap(t *testing.T) {
	e := newEnv(t)
	created, err := e.svc.Bootstrap(e.ctx, "admin", "secret")
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	users, _ := e.svc.ListUsers(e.ctx)
	if len(users) != 1 || users[0].Role != RoleAdmin {
		t.Fatalf("users = %+v", users)
	}
	// Пароль панели не работает в kosync: устройств нет.
	if _, err := e.svc.Authenticate(e.ctx, "admin", md5hex("secret"), client); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("panel password accepted by kosync: %v", err)
	}
	var hash1 string
	if err := e.db.Read.QueryRow(`SELECT panel_password_hash FROM users`).Scan(&hash1); err != nil {
		t.Fatal(err)
	}

	created, err = e.svc.Bootstrap(e.ctx, "admin", "other")
	if err != nil || created {
		t.Fatalf("second bootstrap created=%v err=%v", created, err)
	}
	created, err = e.svc.Bootstrap(e.ctx, "admin2", "other")
	if err != nil || created {
		t.Fatalf("bootstrap with existing users created=%v err=%v", created, err)
	}
	var hash2 string
	if err := e.db.Read.QueryRow(`SELECT panel_password_hash FROM users`).Scan(&hash2); err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Fatal("existing admin changed")
	}
}

func TestBootstrapNeedsBothVars(t *testing.T) {
	e := newEnv(t)
	for _, c := range [][2]string{{"", ""}, {"admin", ""}, {"", "secret"}} {
		created, err := e.svc.Bootstrap(e.ctx, c[0], c[1])
		if err != nil || created {
			t.Fatalf("%v: created=%v err=%v", c, created, err)
		}
	}
}

func TestCreateUserWithDevice(t *testing.T) {
	e := newEnv(t)
	key := md5hex("pw")
	if err := e.svc.CreateUserWithDevice(e.ctx, "alice", "KOReader", key, client); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CreateUserWithDevice(e.ctx, "alice", "KOReader", key, client); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", key, client); err != nil {
		t.Fatal(err)
	}
}

func TestHashFormat(t *testing.T) {
	h, err := hashSecret("key", DefaultArgon2Params)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := verifySecret("key", h)
	if err != nil || !ok {
		t.Fatalf("verify: ok=%v err=%v hash=%s", ok, err, h)
	}
	if ok, _ := verifySecret("other", h); ok {
		t.Fatal("wrong secret verified")
	}
	if want := "$argon2id$v=19$m=19456,t=2,p=1$"; h[:len(want)] != want {
		t.Fatalf("hash = %s", h)
	}
}

// limited — сервис с ограничителем на тех же часах.
func limited(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	lim := ratelimit.New(slog.New(slog.NewJSONHandler(io.Discard, nil)), ratelimit.WithClock(e.clock.now))
	e.svc = New(e.db, WithClock(e.clock.now), WithArgon2Params(TestArgon2Params), WithLimiter(lim))
	return e
}

// block исчерпывает бюджет адреса a разными неверными ключами.
func (e *env) block(t *testing.T, a netip.Addr) {
	t.Helper()
	for i := range ratelimit.MaxFails {
		if _, err := e.svc.Authenticate(e.ctx, "alice", md5hex(fmt.Sprint("guess", i)), a); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
}

func isRateLimited(err error) bool {
	var rl *RateLimitedError
	return errors.As(err, &rl) && errors.Is(err, ErrRateLimited) && !errors.Is(err, ErrUnauthorized) && rl.RetryAfter > 0
}

func TestRateLimitBlockedAddress(t *testing.T) {
	e := limited(t)
	mustUser(t, e, "alice", RoleUser)
	kindle := e.device(t, "alice", "Kindle")
	kobo := e.device(t, "alice", "Kobo")
	if _, err := e.svc.Authenticate(e.ctx, "alice", kindle, client); err != nil {
		t.Fatal(err)
	}
	e.block(t, client)

	// Незакешированный ключ, даже верный, с заблокированного адреса — без argon2.
	before := e.svc.argonCalls.Load()
	if _, err := e.svc.Authenticate(e.ctx, "alice", kobo, client); !isRateLimited(err) {
		t.Fatalf("uncached key from blocked address: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "ghost", kobo, client); !isRateLimited(err) {
		t.Fatalf("unknown user from blocked address: %v", err)
	}
	if e.svc.argonCalls.Load() != before {
		t.Fatal("argon2 ran for a blocked address")
	}
	// Закешированное устройство работает всю блокировку: попадания продлевают кеш.
	for range 5 {
		e.clock.t = e.clock.t.Add(5 * time.Minute)
		if _, err := e.svc.Authenticate(e.ctx, "alice", kindle, client); err != nil {
			t.Fatalf("cached device at +%v: %v", e.clock.t.Sub(time.Unix(1_700_000_000, 0)), err)
		}
	}
	if e.svc.argonCalls.Load() != before {
		t.Fatal("cache expired during active use")
	}
	// Другой адрес не затронут.
	if _, err := e.svc.Authenticate(e.ctx, "alice", kobo, netip.MustParseAddr("192.0.2.2")); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitRepeatedWrongKey(t *testing.T) {
	e := limited(t)
	mustUser(t, e, "alice", RoleUser)
	kindle := e.device(t, "alice", "Kindle")
	old := md5hex("revoked reader")
	before := e.svc.argonCalls.Load()
	for i := range 50 {
		_, err := e.svc.Authenticate(e.ctx, "alice", old, client)
		if !errors.Is(err, ErrUnauthorized) || isRateLimited(err) || errors.Is(err, ErrRepeated) != (i > 0) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if got := e.svc.argonCalls.Load() - before; got != 1 {
		t.Fatalf("%d argon2 calls, want 1", got)
	}
	// Повтор не тратит бюджет: адрес не заблокирован, другая читалка входит.
	if _, err := e.svc.Authenticate(e.ctx, "alice", kindle, client); err != nil {
		t.Fatal(err)
	}
	// Неизвестный пользователь: повтор тоже отвечает ErrUnknownUser.
	for range 3 {
		if _, err := e.svc.Authenticate(e.ctx, "ghost", old, client); !errors.Is(err, ErrUnknownUser) || !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("ghost: %v", err)
		}
	}
	// Запомненная неудача живёт cacheTTL.
	e.clock.t = e.clock.t.Add(cacheTTL + time.Second)
	before = e.svc.argonCalls.Load()
	_, _ = e.svc.Authenticate(e.ctx, "alice", old, client)
	if e.svc.argonCalls.Load() == before {
		t.Fatal("rejected key remembered past TTL")
	}
}

func TestRateLimitForgetOnCredentialChange(t *testing.T) {
	e := limited(t)
	mustUser(t, e, "alice", RoleUser)
	kindle := e.device(t, "alice", "Kindle")
	next := md5hex("next password")
	if _, err := e.svc.Authenticate(e.ctx, "alice", next, client); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	p, err := e.svc.Authenticate(e.ctx, "alice", kindle, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RotateKey(e.ctx, p, next); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", next, client); err != nil {
		t.Fatalf("rotated to a previously rejected key: %v", err)
	}

	// Регистрация с ранее отвергнутым ключом.
	key := md5hex("carol")
	if _, err := e.svc.Authenticate(e.ctx, "carol", key, client); !errors.Is(err, ErrUnknownUser) {
		t.Fatal(err)
	}
	if err := e.svc.CreateUserWithDevice(e.ctx, "carol", "KOReader", key, client); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "carol", key, client); err != nil {
		t.Fatalf("registered with a previously rejected key: %v", err)
	}
}

func TestRateLimitBudget(t *testing.T) {
	e := limited(t)
	mustUser(t, e, "alice", RoleUser)
	mustUser(t, e, "dave", RoleUser)
	kindle := e.device(t, "alice", "Kindle")
	e.device(t, "dave", "Kindle")
	if err := e.svc.DisableUser(e.ctx, "dave"); err != nil {
		t.Fatal(err)
	}
	// Неизвестный и отключённый пользователь расходуют бюджет как неверный ключ.
	for i := range ratelimit.MaxFails - 1 {
		user := []string{"ghost", "dave", "alice"}[i%3]
		if _, err := e.svc.Authenticate(e.ctx, user, md5hex(fmt.Sprint(i)), client); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s: %v", user, err)
		}
	}
	// Удачный незакешированный вход бюджет не тратит.
	for range 3 {
		e.svc.evict(cacheKey(1, kindle))
		if _, err := e.svc.Authenticate(e.ctx, "alice", kindle, client); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Authenticate(e.ctx, "ghost", md5hex("last"), client); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("10th failure: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "dave", md5hex("one more"), client); !isRateLimited(err) {
		t.Fatalf("11th failure: %v", err)
	}
}

func TestRateLimitRegistration(t *testing.T) {
	e := limited(t)
	for i := range ratelimit.MaxFails {
		if err := e.svc.CreateUserWithDevice(e.ctx, fmt.Sprint("user", i), "KOReader", md5hex("pw"), client); err != nil {
			t.Fatal(err)
		}
	}
	before := e.svc.argonCalls.Load()
	if err := e.svc.CreateUserWithDevice(e.ctx, "user10", "KOReader", md5hex("pw"), client); !isRateLimited(err) {
		t.Fatalf("11th registration: %v", err)
	}
	if e.svc.argonCalls.Load() != before {
		t.Fatal("argon2 ran for a blocked registration")
	}
}

func TestNoLimiterUnchanged(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	e.device(t, "alice", "Kindle")
	wrong := md5hex("wrong")
	for i := range 3 * ratelimit.MaxFails {
		before := e.svc.argonCalls.Load()
		_, err := e.svc.Authenticate(e.ctx, "alice", wrong, client)
		if !errors.Is(err, ErrUnauthorized) || isRateLimited(err) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if e.svc.argonCalls.Load() == before {
			t.Fatal("failure remembered without a limiter")
		}
	}
}

func TestDevicePasswordFormat(t *testing.T) {
	re := regexp.MustCompile(`^[a-z2-7]{4}-[a-z2-7]{4}$`)
	seen := map[string]bool{}
	for range 100 {
		p := newDevicePassword()
		if !re.MatchString(p) {
			t.Fatalf("password %q", p)
		}
		seen[p] = true
	}
	if len(seen) < 100 {
		t.Fatal("repeated passwords")
	}
	// Пароль старого формата xxxx-xxxx-xxxx-xxxx по-прежнему проходит проверку.
	e := newEnv(t)
	old := KeyFromPassword("abcd-efgh-ijkl-mnop")
	if err := e.svc.CreateUserWithDevice(e.ctx, "alice", "Kindle", old, client); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", old, client); err != nil {
		t.Fatal(err)
	}
}
