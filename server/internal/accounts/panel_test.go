package accounts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
)

// panelUser создаёт пользователя с паролем панели.
func panelUser(t *testing.T, e *env, name, password string) {
	t.Helper()
	mustUser(t, e, name, RoleUser)
	if err := e.svc.SetPanelPassword(e.ctx, name, password, 0); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyPanelPassword(t *testing.T) {
	e := newEnv(t)
	panelUser(t, e, "alice", "alice-panel")
	mustUser(t, e, "nopass", RoleUser)
	panelUser(t, e, "dave", "dave-panel")
	if err := e.svc.DisableUser(e.ctx, "dave"); err != nil {
		t.Fatal(err)
	}
	device, err := e.svc.AddDevice(e.ctx, "alice", "Kindle")
	if err != nil {
		t.Fatal(err)
	}

	u, err := e.svc.VerifyPanelPassword(e.ctx, "alice", "alice-panel", client)
	if err != nil || u.Username != "alice" || u.Role != RoleUser || u.ID == 0 {
		t.Fatalf("valid password: %+v %v", u, err)
	}
	for _, c := range []struct{ user, password string }{
		{"alice", "wrong-password"},
		{"alice", device}, // пароль устройства
		{"alice", md5hex(device)},
		{"ghost", "alice-panel"},
		{"nopass", "anything-at-all"},
		{"dave", "dave-panel"}, // отключён
	} {
		before := e.svc.argonCalls.Load()
		if _, err := e.svc.VerifyPanelPassword(e.ctx, c.user, c.password, client); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s/%s: %v", c.user, c.password, err)
		}
		if got := e.svc.argonCalls.Load() - before; got != 1 {
			t.Fatalf("%s/%s: %d argon2 calls, want 1", c.user, c.password, got)
		}
	}
}

func TestVerifyPanelPasswordRateLimit(t *testing.T) {
	e := limited(t)
	panelUser(t, e, "alice", "alice-panel")
	// Удача бюджет не тратит.
	for range 3 {
		if _, err := e.svc.VerifyPanelPassword(e.ctx, "alice", "alice-panel", client); err != nil {
			t.Fatal(err)
		}
	}
	// Повтор одного и того же неверного пароля тратит бюджет каждый раз.
	for i := range ratelimit.MaxFails {
		user := []string{"alice", "ghost"}[i%2]
		if _, err := e.svc.VerifyPanelPassword(e.ctx, user, "same-guess", client); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	before := e.svc.argonCalls.Load()
	if _, err := e.svc.VerifyPanelPassword(e.ctx, "alice", "alice-panel", client); !isRateLimited(err) {
		t.Fatalf("blocked address: %v", err)
	}
	if e.svc.argonCalls.Load() != before {
		t.Fatal("argon2 ran for a blocked address")
	}
}

func TestSetPanelPassword(t *testing.T) {
	e := newEnv(t)
	panelUser(t, e, "alice", "first-password")
	panelUser(t, e, "bob", "bob-password")
	a1, s1, _ := e.svc.CreateSession(e.ctx, userID(t, e, "alice"))
	a2, _, _ := e.svc.CreateSession(e.ctx, userID(t, e, "alice"))
	b1, _, _ := e.svc.CreateSession(e.ctx, userID(t, e, "bob"))

	for _, bad := range []string{"short", strings.Repeat("x", MaxPanelPassword+1)} {
		if err := e.svc.SetPanelPassword(e.ctx, "alice", bad, 0); !errors.Is(err, ErrInvalidPassword) || !errors.Is(err, ErrInvalid) {
			t.Fatalf("len %d: %v", len(bad), err)
		}
	}
	if _, err := e.svc.VerifyPanelPassword(e.ctx, "alice", "first-password", client); err != nil {
		t.Fatal("password changed by invalid input")
	}
	// Длина — в символах, а не байтах.
	if err := e.svc.SetPanelPassword(e.ctx, "alice", strings.Repeat("я", MaxPanelPassword), s1.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetPanelPassword(e.ctx, "alice", "second-password", s1.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetPanelPassword(e.ctx, "ghost", "second-password", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	if _, err := e.svc.VerifyPanelPassword(e.ctx, "alice", "first-password", client); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old password: %v", err)
	}
	if _, err := e.svc.VerifyPanelPassword(e.ctx, "alice", "second-password", client); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if _, _, err := e.svc.SessionByToken(e.ctx, a1); err != nil {
		t.Fatalf("kept session: %v", err)
	}
	if _, _, err := e.svc.SessionByToken(e.ctx, a2); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("other session survived: %v", err)
	}
	if _, _, err := e.svc.SessionByToken(e.ctx, b1); err != nil {
		t.Fatalf("another user's session: %v", err)
	}
	// keepSession = 0 удаляет все.
	if err := e.svc.SetPanelPassword(e.ctx, "alice", "third-password", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.SessionByToken(e.ctx, a1); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("session after CLI reset: %v", err)
	}
}

func TestCreateFirstAdmin(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.CreateFirstAdmin(e.ctx, "a:b", "long-password"); !errors.Is(err, ErrInvalidUsername) {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := e.svc.CreateFirstAdmin(e.ctx, "admin", "short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("short password: %v", err)
	}
	u, err := e.svc.CreateFirstAdmin(e.ctx, "admin", "admin-password")
	if err != nil || u.Role != RoleAdmin || u.ID == 0 {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := e.svc.VerifyPanelPassword(e.ctx, "admin", "admin-password", client); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateFirstAdmin(e.ctx, "other", "other-password"); !errors.Is(err, ErrSetupCompleted) {
		t.Fatalf("second admin: %v", err)
	}
}

func TestCreateFirstAdminRace(t *testing.T) {
	e := newEnv(t)
	other := e.cli(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		svc := e.svc
		if i%2 == 1 {
			svc = other
		}
		wg.Go(func() {
			_, errs[i] = svc.CreateFirstAdmin(e.ctx, fmt.Sprint("admin", i), "admin-password")
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrSetupCompleted):
			t.Fatal(err)
		}
	}
	if n, _ := e.svc.CountUsers(e.ctx); ok != 1 || n != 1 {
		t.Fatalf("%d succeeded, %d users", ok, n)
	}
}

func userID(t *testing.T, e *env, name string) int64 {
	t.Helper()
	var id int64
	if err := e.db.Read.QueryRow(`SELECT id FROM users WHERE username = ?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSessions(t *testing.T) {
	e := newEnv(t)
	panelUser(t, e, "alice", "alice-panel")
	id := userID(t, e, "alice")
	token, sess, err := e.svc.CreateSession(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 43 || sess.UserID != id || !sess.ExpiresAt.Equal(e.clock.t.Add(SessionTTL)) {
		t.Fatalf("token %q, session %+v", token, sess)
	}
	got, u, err := e.svc.SessionByToken(e.ctx, token)
	if err != nil || got.ID != sess.ID || u.Username != "alice" || got.Renewed {
		t.Fatalf("lookup: %+v %+v %v", got, u, err)
	}
	for _, bad := range []string{"", token + "x", "nope"} {
		if _, _, err := e.svc.SessionByToken(e.ctx, bad); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%q: %v", bad, err)
		}
	}

	// Токена в базе нет, только хеш.
	for _, suffix := range []string{"", "-wal"} {
		if data, err := os.ReadFile(e.path + suffix); err == nil && bytes.Contains(data, []byte(token)) {
			t.Fatalf("token found in database%s", suffix)
		}
	}

	if err := e.svc.DeleteSession(e.ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.SessionByToken(e.ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("deleted session: %v", err)
	}
	if err := e.svc.DeleteSession(e.ctx, sess.ID); err != nil {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestSessionRenewal(t *testing.T) {
	e := newEnv(t)
	panelUser(t, e, "alice", "alice-panel")
	token, _, _ := e.svc.CreateSession(e.ctx, userID(t, e, "alice"))
	t0 := e.clock.t

	e.clock.t = t0.Add(30 * time.Second)
	if s, _, err := e.svc.SessionByToken(e.ctx, token); err != nil || s.Renewed || !s.ExpiresAt.Equal(t0.Add(SessionTTL)) {
		t.Fatalf("within a minute: %+v %v", s, err)
	}
	e.clock.t = t0.Add(61 * time.Second)
	if s, _, err := e.svc.SessionByToken(e.ctx, token); err != nil || !s.Renewed || !s.ExpiresAt.Equal(e.clock.t.Add(SessionTTL)) {
		t.Fatalf("after a minute: %+v %v", s, err)
	}
	// Использование продлевает: через 29 дней после продления сессия жива.
	e.clock.t = e.clock.t.Add(SessionTTL - 24*time.Hour)
	if _, _, err := e.svc.SessionByToken(e.ctx, token); err != nil {
		t.Fatalf("renewed session: %v", err)
	}
	// А 30 дней без использования — истекает.
	e.clock.t = e.clock.t.Add(SessionTTL)
	if _, _, err := e.svc.SessionByToken(e.ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired session: %v", err)
	}
}

func TestSessionDisabledAndDeletedUser(t *testing.T) {
	e := newEnv(t)
	panelUser(t, e, "alice", "alice-panel")
	panelUser(t, e, "bob", "bob-password")
	alice, _, _ := e.svc.CreateSession(e.ctx, userID(t, e, "alice"))
	bob, _, _ := e.svc.CreateSession(e.ctx, userID(t, e, "bob"))
	cli := e.cli(t)
	if err := cli.DisableUser(e.ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cli.DeleteUser(e.ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"disabled": alice, "deleted": bob} {
		if _, _, err := e.svc.SessionByToken(e.ctx, token); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s user: %v", name, err)
		}
	}
	var n int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("sessions after delete: %d %v", n, err)
	}
}

func TestSessionCleanup(t *testing.T) {
	e := newEnv(t)
	panelUser(t, e, "alice", "alice-panel")
	id := userID(t, e, "alice")
	for range 3 {
		if _, _, err := e.svc.CreateSession(e.ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.t = e.clock.t.Add(SessionTTL + time.Second)
	if _, _, err := e.svc.CreateSession(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("sessions after cleanup: %d %v", n, err)
	}
}

func TestGeneratePassword(t *testing.T) {
	for groups, re := range map[int]*regexp.Regexp{
		2: regexp.MustCompile(`^[a-z2-7]{4}-[a-z2-7]{4}$`),
		3: regexp.MustCompile(`^[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}$`),
		4: regexp.MustCompile(`^[a-z2-7]{4}(-[a-z2-7]{4}){3}$`),
	} {
		if p := GeneratePassword(groups); !re.MatchString(p) {
			t.Fatalf("%d groups: %q", groups, p)
		}
	}
}
