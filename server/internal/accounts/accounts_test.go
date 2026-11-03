package accounts

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

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

	p1, err := e.svc.Authenticate(e.ctx, "alice", kindle)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := e.svc.Authenticate(e.ctx, "alice", kobo)
	if err != nil {
		t.Fatal(err)
	}
	if p1.UserID != p2.UserID || p1.CredentialID == p2.CredentialID {
		t.Fatalf("principals %+v %+v", p1, p2)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", md5hex("wrong")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "ghost", kindle); !errors.Is(err, ErrUnknownUser) || !errors.Is(err, ErrUnauthorized) {
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
		if _, err := e.svc.Authenticate(e.ctx, "alice", k); err != nil {
			t.Fatal(err)
		}
	}
	before := e.svc.argonCalls.Load()
	if _, err := e.svc.Authenticate(e.ctx, "alice", kindle); err != nil {
		t.Fatal(err)
	}
	if e.svc.argonCalls.Load() != before {
		t.Fatal("cached check ran argon2")
	}

	cli := e.cli(t)
	if err := cli.RevokeDevice(e.ctx, "alice", "Kindle"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kindle); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked device still authorized: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kobo); err != nil {
		t.Fatalf("other device broken: %v", err)
	}

	if err := cli.DisableUser(e.ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kobo); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled user authorized: %v", err)
	}
}

func TestRotateKey(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	kindle := e.device(t, "alice", "Kindle")
	kobo := e.device(t, "alice", "Kobo")
	p, err := e.svc.Authenticate(e.ctx, "alice", kindle)
	if err != nil {
		t.Fatal(err)
	}
	// Смена ключа другим экземпляром сервиса: кеш первого о ней не знает.
	cli := e.cli(t)
	newKey := md5hex("new password")
	if err := cli.RotateKey(e.ctx, p, newKey); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kindle); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old cached key works: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", newKey); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", kobo); err != nil {
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
	p, err := e.svc.Authenticate(e.ctx, "alice", key)
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
	if _, err := e.svc.Authenticate(e.ctx, "alice", key); !errors.Is(err, ErrUnknownUser) {
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
		_, _ = e.svc.Authenticate(e.ctx, c.user, md5hex("wrong"))
		if got := e.svc.argonCalls.Load() - before; got != 1 {
			t.Fatalf("%s: %d argon2 calls, want 1", c.user, got)
		}
	}
	// Пользователь без устройств: тоже одна фиктивная проверка.
	mustUser(t, e, "bob", RoleUser)
	before := e.svc.argonCalls.Load()
	_, _ = e.svc.Authenticate(e.ctx, "bob", md5hex("wrong"))
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
	if err := e.svc.CreateUserWithDevice(e.ctx, "bob", "KOReader", md5hex("bobpass")); err != nil {
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
	if _, err := e.svc.Authenticate(e.ctx, "alice", key); err != nil {
		t.Fatal(err)
	}
	if lastUsed() != t0.Unix() {
		t.Fatal("first use not recorded")
	}
	e.clock.t = t0.Add(30 * time.Second)
	writes := e.svc.lastUsedWrites.Load()
	if _, err := e.svc.Authenticate(e.ctx, "alice", key); err != nil {
		t.Fatal(err)
	}
	if lastUsed() != t0.Unix() || e.svc.lastUsedWrites.Load() != writes {
		t.Fatal("last_used_at rewritten within a minute")
	}
	e.clock.t = t0.Add(61 * time.Second)
	if _, err := e.svc.Authenticate(e.ctx, "alice", key); err != nil {
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
	if _, err := e.svc.Authenticate(e.ctx, "admin", md5hex("secret")); !errors.Is(err, ErrUnauthorized) {
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
	if err := e.svc.CreateUserWithDevice(e.ctx, "alice", "KOReader", key); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CreateUserWithDevice(e.ctx, "alice", "KOReader", key); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", key); err != nil {
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
