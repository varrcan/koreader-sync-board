package accounts

import (
	"errors"
	"sync"
	"testing"
)

const (
	issuer      = "https://id.example.com"
	otherIssuer = "https://other.example.com"
)

func TestUserByIdentity(t *testing.T) {
	e := newEnv(t)
	u, err := e.svc.CreateOIDCUser(e.ctx, "bob", RoleUser, issuer, "123")
	if err != nil {
		t.Fatal(err)
	}
	if !u.OIDCLinked || u.HasPassword || u.Role != RoleUser {
		t.Fatalf("created %+v", u)
	}
	got, err := e.svc.UserByIdentity(e.ctx, issuer, "123")
	if err != nil || got.ID != u.ID || got.Username != "bob" || !got.OIDCLinked || got.HasPassword {
		t.Fatalf("found %+v %v", got, err)
	}
	for _, c := range [][2]string{{issuer, "456"}, {otherIssuer, "123"}} {
		if _, err := e.svc.UserByIdentity(e.ctx, c[0], c[1]); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%v: %v", c, err)
		}
	}
	// Отключённый находится: обработчик отвечает account_disabled.
	if err := e.svc.DisableUser(e.ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.UserByIdentity(e.ctx, issuer, "123"); err != nil || got.DisabledAt == nil {
		t.Fatalf("disabled %+v %v", got, err)
	}
}

func TestCreateOIDCUser(t *testing.T) {
	e := newEnv(t)
	panelUser(t, e, "alice", "alice-panel")
	if _, err := e.svc.CreateOIDCUser(e.ctx, "alice", RoleUser, issuer, "1"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("taken name: %v", err)
	}
	if u, _ := e.svc.VerifyPanelPassword(e.ctx, "alice", "alice-panel", client); u.OIDCLinked {
		t.Fatal("existing user linked")
	}
	for _, name := range []string{"", "a:b"} {
		if _, err := e.svc.CreateOIDCUser(e.ctx, name, RoleUser, issuer, "1"); !errors.Is(err, ErrInvalidUsername) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if _, err := e.svc.CreateOIDCUser(e.ctx, "carol", RoleAdmin, issuer, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateOIDCUser(e.ctx, "carol2", RoleUser, issuer, "1"); !errors.Is(err, ErrIdentityTaken) {
		t.Fatalf("same sub: %v", err)
	}
	// Тот же sub у другого issuer — другая учётка.
	if _, err := e.svc.CreateOIDCUser(e.ctx, "carol3", RoleUser, otherIssuer, "1"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOIDCUserRace(t *testing.T) {
	e := newEnv(t)
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { _, errs[i] = e.svc.CreateOIDCUser(e.ctx, "bob", RoleUser, issuer, "123") })
	}
	wg.Wait()
	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrIdentityTaken):
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	users, err := e.svc.ListUsers(e.ctx)
	if created != 1 || err != nil || len(users) != 1 {
		t.Fatalf("created %d, users %+v %v", created, users, err)
	}
}

func TestLinkIdentity(t *testing.T) {
	e := newEnv(t)
	panelUser(t, e, "alice", "alice-panel")
	panelUser(t, e, "bob", "bob-panel")
	alice, _ := e.svc.VerifyPanelPassword(e.ctx, "alice", "alice-panel", client)
	bob, _ := e.svc.VerifyPanelPassword(e.ctx, "bob", "bob-panel", client)

	if err := e.svc.LinkIdentity(e.ctx, alice.ID, issuer, "1"); err != nil {
		t.Fatal(err)
	}
	// Повторная привязка той же пары — не ошибка.
	if err := e.svc.LinkIdentity(e.ctx, alice.ID, issuer, "1"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.LinkIdentity(e.ctx, bob.ID, issuer, "1"); !errors.Is(err, ErrIdentityTaken) {
		t.Fatalf("taken: %v", err)
	}
	if u, err := e.svc.UserByIdentity(e.ctx, issuer, "1"); err != nil || u.ID != alice.ID {
		t.Fatalf("after conflict %+v %v", u, err)
	}
	// Привязка к другой паре заменяет прежнюю.
	if err := e.svc.LinkIdentity(e.ctx, alice.ID, issuer, "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UserByIdentity(e.ctx, issuer, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old pair: %v", err)
	}
	if err := e.svc.LinkIdentity(e.ctx, bob.ID, issuer, "1"); err != nil {
		t.Fatalf("freed pair: %v", err)
	}
	if err := e.svc.LinkIdentity(e.ctx, 9999, issuer, "3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestUnlinkIdentity(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.CreateOIDCUser(e.ctx, "bob", RoleUser, issuer, "1"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UnlinkIdentity(e.ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UserByIdentity(e.ctx, issuer, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after unlink: %v", err)
	}
	// Повторная отвязка — не ошибка, неизвестный пользователь — ErrNotFound.
	if err := e.svc.UnlinkIdentity(e.ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UnlinkIdentity(e.ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ghost: %v", err)
	}
}

func TestSetRole(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	if err := e.svc.SetRole(e.ctx, "alice", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if users, _ := e.svc.ListUsers(e.ctx); users[0].Role != RoleAdmin {
		t.Fatalf("role %v", users[0].Role)
	}
	if err := e.svc.SetRole(e.ctx, "alice", "root"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad role: %v", err)
	}
	if err := e.svc.SetRole(e.ctx, "ghost", RoleUser); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ghost: %v", err)
	}
}

func TestSetFirstPanelPassword(t *testing.T) {
	e := newEnv(t)
	bob, err := e.svc.CreateOIDCUser(e.ctx, "bob", RoleUser, issuer, "1")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := e.svc.CreateSession(e.ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetFirstPanelPassword(e.ctx, bob.ID, "short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("short: %v", err)
	}
	if err := e.svc.SetFirstPanelPassword(e.ctx, bob.ID, "bob-first-pass"); err != nil {
		t.Fatal(err)
	}
	// Сессии не трогаются.
	if _, u, err := e.svc.SessionByToken(e.ctx, token); err != nil || !u.HasPassword || !u.OIDCLinked {
		t.Fatalf("session after first password %+v %v", u, err)
	}
	if _, err := e.svc.VerifyPanelPassword(e.ctx, "bob", "bob-first-pass", client); err != nil {
		t.Fatal(err)
	}
	// Пароль уже задан: второй «первый» не проходит.
	if err := e.svc.SetFirstPanelPassword(e.ctx, bob.ID, "another-pass"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("second first password: %v", err)
	}
}

func TestUserFlags(t *testing.T) {
	e := newEnv(t)
	panelUser(t, e, "alice", "alice-panel")
	mustUser(t, e, "carol", RoleUser)
	bob, err := e.svc.CreateOIDCUser(e.ctx, "bob", RoleUser, issuer, "1")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := e.svc.VerifyPanelPassword(e.ctx, "alice", "alice-panel", client)
	if err != nil || !alice.HasPassword || alice.OIDCLinked {
		t.Fatalf("verify %+v %v", alice, err)
	}
	if err := e.svc.LinkIdentity(e.ctx, alice.ID, issuer, "2"); err != nil {
		t.Fatal(err)
	}
	if u, _ := e.svc.VerifyPanelPassword(e.ctx, "alice", "alice-panel", client); !u.OIDCLinked {
		t.Fatalf("verify linked %+v", u)
	}
	users, err := e.svc.ListUsers(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]bool{"alice": {true, true}, "bob": {false, true}, "carol": {false, false}}
	for _, u := range users {
		if w := want[u.Username]; u.HasPassword != w[0] || u.OIDCLinked != w[1] {
			t.Fatalf("list %s: %+v", u.Username, u)
		}
	}
	token, _, _ := e.svc.CreateSession(e.ctx, bob.ID)
	if _, u, err := e.svc.SessionByToken(e.ctx, token); err != nil || u.HasPassword || !u.OIDCLinked {
		t.Fatalf("session %+v %v", u, err)
	}
	if admin, err := newEnv(t).svc.CreateFirstAdmin(e.ctx, "admin", "admin-password"); err != nil || !admin.HasPassword {
		t.Fatalf("first admin %+v %v", admin, err)
	}
}
