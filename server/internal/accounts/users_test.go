package accounts

import (
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"
)

var issuedPassword = regexp.MustCompile(`^[a-z2-7]{4}(-[a-z2-7]{4}){3}$`)

func ptr[T any](v T) *T { return &v }

// admin создаёт действующего администратора и возвращает его id.
func (e *env) admin(t *testing.T, name string) int64 {
	t.Helper()
	mustUser(t, e, name, RoleAdmin)
	return userID(t, e, name)
}

func (e *env) session(t *testing.T, id int64) string {
	t.Helper()
	token, _, err := e.svc.CreateSession(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (e *env) sessionWorks(token string) bool {
	_, _, err := e.svc.SessionByToken(e.ctx, token)
	return err == nil
}

func (e *env) userByName(t *testing.T, name string) User {
	t.Helper()
	u, err := e.svc.UserByID(e.ctx, userID(t, e, name))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// adminActions — все действия над существующим пользователем, кроме создания.
func adminActions(e *env) map[string]func(actor, target int64) error {
	return map[string]func(actor, target int64) error{
		"reset": func(a, t int64) error { _, _, err := e.svc.ResetPanelPassword(e.ctx, a, t); return err },
		"role": func(a, t int64) error {
			_, err := e.svc.UpdateUser(e.ctx, a, t, UserUpdate{Role: ptr(RoleUser)})
			return err
		},
		"disable": func(a, t int64) error {
			_, err := e.svc.UpdateUser(e.ctx, a, t, UserUpdate{Disabled: ptr(true)})
			return err
		},
		"unlink": func(a, t int64) error { _, err := e.svc.AdminUnlinkIdentity(e.ctx, a, t); return err },
		"delete": func(a, t int64) error { _, err := e.svc.AdminDeleteUser(e.ctx, a, t); return err },
	}
}

func TestAdminSelfAction(t *testing.T) {
	e := newEnv(t)
	root := e.admin(t, "root")
	token := e.session(t, root)
	for name, do := range adminActions(e) {
		if err := do(root, root); !errors.Is(err, ErrSelf) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if u := e.userByName(t, "root"); u.Role != RoleAdmin || u.DisabledAt != nil || u.HasPassword {
		t.Fatalf("root changed: %+v", u)
	}
	if !e.sessionWorks(token) {
		t.Fatal("session lost")
	}
}

func TestAdminActorChecked(t *testing.T) {
	for _, tc := range []struct {
		name string
		lose func(e *env, username string) error
	}{
		{"demoted", func(e *env, n string) error { return e.svc.SetRole(e.ctx, n, RoleUser) }},
		{"disabled", func(e *env, n string) error { return e.svc.DisableUser(e.ctx, n) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			root := e.admin(t, "root")
			bob := e.admin(t, "bob")
			token := e.session(t, bob)
			if err := tc.lose(e, "root"); err != nil {
				t.Fatal(err)
			}
			for name, do := range adminActions(e) {
				if err := do(root, bob); !errors.Is(err, ErrForbidden) {
					t.Fatalf("%s: %v", name, err)
				}
			}
			if u := e.userByName(t, "bob"); u.Role != RoleAdmin || u.DisabledAt != nil || u.HasPassword {
				t.Fatalf("bob changed: %+v", u)
			}
			if !e.sessionWorks(token) {
				t.Fatal("bob's session lost")
			}
		})
	}

	e := newEnv(t)
	root := e.admin(t, "root")
	for name, do := range adminActions(e) {
		if err := do(root, 999); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("%s unknown target: %v", name, err)
		}
	}
}

func TestMutualDemotionLeavesOneAdmin(t *testing.T) {
	for range 20 {
		e := newEnv(t)
		alice := e.admin(t, "alice")
		bob := e.admin(t, "bob")
		start := make(chan struct{})
		var wg sync.WaitGroup
		var errs [2]error
		for i, pair := range [][2]int64{{alice, bob}, {bob, alice}} {
			wg.Go(func() {
				<-start
				_, errs[i] = e.svc.UpdateUser(e.ctx, pair[0], pair[1], UserUpdate{Role: ptr(RoleUser)})
			})
		}
		close(start)
		wg.Wait()
		ok, forbidden := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrForbidden):
				forbidden++
			default:
				t.Fatal(err)
			}
		}
		var admins int
		if err := e.db.Read.QueryRow(`SELECT count(*) FROM users WHERE role = 'admin'`).Scan(&admins); err != nil {
			t.Fatal(err)
		}
		if ok != 1 || forbidden != 1 || admins != 1 {
			t.Fatalf("ok=%d forbidden=%d admins=%d", ok, forbidden, admins)
		}
	}
}

func TestCreateUserWithPassword(t *testing.T) {
	e := limited(t)
	root := e.admin(t, "root")

	// Неудача имени запоминается и должна быть забыта при создании.
	if _, err := e.svc.Authenticate(e.ctx, "carol", md5hex("k"), client); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	u, pass, err := e.svc.CreateUserWithPassword(e.ctx, root, "carol", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if !issuedPassword.MatchString(pass) || u.Username != "carol" || u.Role != RoleUser || !u.HasPassword {
		t.Fatalf("%+v %q", u, pass)
	}
	if _, err := e.svc.VerifyPanelPassword(e.ctx, "carol", pass, client); err != nil {
		t.Fatalf("login: %v", err)
	}
	e.svc.mu.Lock()
	left := len(e.svc.rejected)
	e.svc.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d rejected entries left", left)
	}

	if _, _, err := e.svc.CreateUserWithPassword(e.ctx, root, "carol", RoleAdmin); !errors.Is(err, ErrUserExists) {
		t.Fatalf("taken: %v", err)
	}
	if got := e.userByName(t, "carol"); got.Role != RoleUser {
		t.Fatalf("existing user changed: %+v", got)
	}
	if _, _, err := e.svc.CreateUserWithPassword(e.ctx, root, "a:b", RoleUser); !errors.Is(err, ErrInvalidUsername) {
		t.Fatalf("bad name: %v", err)
	}
	if _, _, err := e.svc.CreateUserWithPassword(e.ctx, root, "dan", "root"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad role: %v", err)
	}

	if err := e.svc.SetRole(e.ctx, "root", RoleUser); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.CreateUserWithPassword(e.ctx, root, "erin", RoleUser); !errors.Is(err, ErrForbidden) {
		t.Fatalf("lost rights: %v", err)
	}
	// Занятое имя у потерявшего права — тоже ErrForbidden: он не узнаёт о занятых именах.
	if _, _, err := e.svc.CreateUserWithPassword(e.ctx, root, "carol", RoleUser); !errors.Is(err, ErrForbidden) {
		t.Fatalf("lost rights, taken name: %v", err)
	}
	var n int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM users WHERE username = 'erin'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("erin created: %d %v", n, err)
	}
}

func TestResetPanelPassword(t *testing.T) {
	e := newEnv(t)
	root := e.admin(t, "root")
	panelUser(t, e, "bob", "old-password")
	key := e.device(t, "bob", "Kindle")
	bob := userID(t, e, "bob")
	first, second := e.session(t, bob), e.session(t, bob)

	name, pass, err := e.svc.ResetPanelPassword(e.ctx, root, bob)
	if err != nil || name != "bob" || !issuedPassword.MatchString(pass) {
		t.Fatalf("%q %q %v", name, pass, err)
	}
	if e.sessionWorks(first) || e.sessionWorks(second) {
		t.Fatal("sessions survived")
	}
	if _, err := e.svc.VerifyPanelPassword(e.ctx, "bob", "old-password", client); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old password: %v", err)
	}
	if _, err := e.svc.VerifyPanelPassword(e.ctx, "bob", pass, client); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "bob", key, client); err != nil {
		t.Fatalf("device key: %v", err)
	}
}

func TestUpdateUserRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	root := e.admin(t, "root")
	mustUser(t, e, "bob", RoleUser)
	bob := userID(t, e, "bob")
	if _, err := e.svc.UpdateUser(e.ctx, root, bob, UserUpdate{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := e.svc.UpdateUser(e.ctx, root, bob, UserUpdate{Role: ptr(Role("root"))}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad role: %v", err)
	}
}

func TestUpdateUserRoleAndDisable(t *testing.T) {
	e := newEnv(t)
	root := e.admin(t, "root")
	mustUser(t, e, "bob", RoleUser)
	bob := userID(t, e, "bob")

	if name, err := e.svc.UpdateUser(e.ctx, root, bob, UserUpdate{Role: ptr(RoleAdmin)}); err != nil || name != "bob" {
		t.Fatalf("%q %v", name, err)
	}
	if u := e.userByName(t, "bob"); u.Role != RoleAdmin || u.DisabledAt != nil {
		t.Fatalf("after role: %+v", u)
	}

	if _, err := e.svc.UpdateUser(e.ctx, root, bob, UserUpdate{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	first := e.userByName(t, "bob").DisabledAt
	if first == nil || e.userByName(t, "bob").Role != RoleAdmin {
		t.Fatalf("after disable: %+v", e.userByName(t, "bob"))
	}
	e.clock.t = e.clock.t.Add(time.Hour)
	if _, err := e.svc.UpdateUser(e.ctx, root, bob, UserUpdate{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if again := e.userByName(t, "bob").DisabledAt; again == nil || !again.Equal(*first) {
		t.Fatalf("disabled_at moved: %v → %v", first, again)
	}
}

// Включение одинаково из панели и из CLI, различаются только адресация и актор.
func enablers(e *env, root, target int64, username string) map[string]func() error {
	return map[string]func() error{
		"panel": func() error {
			_, err := e.svc.UpdateUser(e.ctx, root, target, UserUpdate{Disabled: ptr(false)})
			return err
		},
		"service": func() error { return e.svc.EnableUser(e.ctx, username) },
	}
}

func TestEnableUser(t *testing.T) {
	for _, via := range []string{"panel", "service"} {
		t.Run(via, func(t *testing.T) {
			// Медленный путь: удачной проверки в кеше нет, отказ запомнен.
			e := limited(t)
			root := e.admin(t, "root")
			mustUser(t, e, "bob", RoleUser)
			key := e.device(t, "bob", "Kindle")
			bob := userID(t, e, "bob")
			token := e.session(t, bob)
			enable := enablers(e, root, bob, "bob")[via]
			if err := e.svc.DisableUser(e.ctx, "bob"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.Authenticate(e.ctx, "bob", key, client); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("disabled: %v", err)
			}
			if _, err := e.svc.Authenticate(e.ctx, "bob", key, client); !errors.Is(err, ErrRepeated) {
				t.Fatalf("not remembered: %v", err)
			}
			if err := enable(); err != nil {
				t.Fatal(err)
			}
			if u := e.userByName(t, "bob"); u.DisabledAt != nil {
				t.Fatalf("still disabled: %+v", u)
			}
			if e.sessionWorks(token) {
				t.Fatal("old session revived")
			}
			if _, err := e.svc.Authenticate(e.ctx, "bob", key, client); err != nil {
				t.Fatalf("slow path: %v", err)
			}

			// Кешированный путь: ключ уже проверен, argon2 не нужен.
			if err := e.svc.DisableUser(e.ctx, "bob"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.Authenticate(e.ctx, "bob", key, client); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("disabled again: %v", err)
			}
			if err := enable(); err != nil {
				t.Fatal(err)
			}
			before := e.svc.argonCalls.Load()
			if _, err := e.svc.Authenticate(e.ctx, "bob", key, client); err != nil {
				t.Fatalf("cached path: %v", err)
			}
			if got := e.svc.argonCalls.Load() - before; got != 0 {
				t.Fatalf("%d argon2 calls on cached path", got)
			}
		})
	}
}

func TestEnableNotDisabledKeepsSessions(t *testing.T) {
	e := limited(t)
	root := e.admin(t, "root")
	mustUser(t, e, "bob", RoleUser)
	bob := userID(t, e, "bob")
	token := e.session(t, bob)

	if _, err := e.svc.UpdateUser(e.ctx, root, bob, UserUpdate{Disabled: ptr(false), Role: ptr(RoleAdmin)}); err != nil {
		t.Fatal(err)
	}
	if u := e.userByName(t, "bob"); u.Role != RoleAdmin || u.DisabledAt != nil {
		t.Fatalf("after patch: %+v", u)
	}
	if err := e.svc.EnableUser(e.ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if !e.sessionWorks(token) {
		t.Fatal("session lost")
	}
	if err := e.svc.EnableUser(e.ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestUsersList(t *testing.T) {
	e := newEnv(t)
	base := e.clock.t
	mustUser(t, e, "bob", RoleUser)
	mustUser(t, e, "carol", RoleUser)
	mustUser(t, e, "dave", RoleUser)

	kindle := e.device(t, "bob", "Kindle")
	e.device(t, "bob", "Kobo")
	if _, err := e.svc.Authenticate(e.ctx, "bob", kindle, client); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeDevice(e.ctx, "bob", "Kindle"); err != nil {
		t.Fatal(err)
	}

	e.clock.t = base.Add(time.Hour)
	key := e.device(t, "carol", "Boox")
	if _, err := e.svc.Authenticate(e.ctx, "carol", key, client); err != nil {
		t.Fatal(err)
	}
	e.clock.t = base.Add(2 * time.Hour)
	e.session(t, userID(t, e, "carol"))

	users, err := e.svc.Users(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 || users[0].Username != "bob" || users[1].Username != "carol" || users[2].Username != "dave" {
		t.Fatalf("%+v", users)
	}
	// Активность bob — только у отозванного устройства.
	if b := users[0]; b.DeviceCount != 1 || b.LastActiveAt == nil || !b.LastActiveAt.Equal(base) {
		t.Fatalf("bob %+v", b)
	}
	if c := users[1]; c.DeviceCount != 1 || c.LastActiveAt == nil || !c.LastActiveAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("carol %+v", c)
	}
	if d := users[2]; d.DeviceCount != 0 || d.LastActiveAt != nil {
		t.Fatalf("dave %+v", d)
	}
}

func TestAdminDeleteCascades(t *testing.T) {
	e := newEnv(t)
	root := e.admin(t, "root")
	mustUser(t, e, "bob", RoleUser)
	e.device(t, "bob", "Kindle")
	bob := userID(t, e, "bob")
	e.session(t, bob)
	for _, q := range []string{
		`INSERT INTO documents (id, hash, first_seen_at) VALUES (7, 'h', 1)`,
		`INSERT INTO progress_events (id, user_id, document_id, credential_id, percentage, progress, device, server_ts) SELECT 1, ?1, 7, id, 0.5, '1', 'd', 1 FROM device_credentials WHERE user_id = ?1`,
		`INSERT INTO progress_current (user_id, document_id, event_id) VALUES (?1, 7, 1)`,
		`INSERT INTO webdav_files (user_id, name, content, etag, size, modified_at, credential_id, source) SELECT ?1, 'f', x'00', 'e', 1, 1, id, 'webdav' FROM device_credentials WHERE user_id = ?1`,
		`INSERT INTO stat_books (id, user_id) VALUES (1, ?1)`,
		`INSERT INTO stat_pages (book_id, user_id, page, start_time, duration, total_pages) VALUES (1, ?1, 1, 1, 1, 1)`,
	} {
		if _, err := e.db.Write.ExecContext(e.ctx, q, bob); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	name, err := e.svc.AdminDeleteUser(e.ctx, root, bob)
	if err != nil || name != "bob" {
		t.Fatalf("%q %v", name, err)
	}
	for _, table := range []string{"device_credentials", "sessions", "progress_events", "progress_current", "webdav_files", "stat_books", "stat_pages"} {
		var n int
		if err := e.db.Read.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s: %d rows left", table, n)
		}
	}
	var docs int
	if err := e.db.Read.QueryRow(`SELECT count(*) FROM documents`).Scan(&docs); err != nil || docs != 1 {
		t.Fatalf("documents: %d %v", docs, err)
	}
	if _, err := e.svc.AdminDeleteUser(e.ctx, root, bob); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAdminUnlinkIdentity(t *testing.T) {
	e := newEnv(t)
	root := e.admin(t, "root")
	u, err := e.svc.CreateOIDCUser(e.ctx, "bob", RoleUser, "https://id.example.com", "1")
	if err != nil {
		t.Fatal(err)
	}
	if name, err := e.svc.AdminUnlinkIdentity(e.ctx, root, u.ID); err != nil || name != "bob" {
		t.Fatalf("%q %v", name, err)
	}
	if _, err := e.svc.UserByIdentity(e.ctx, "https://id.example.com", "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still linked: %v", err)
	}
	if _, err := e.svc.AdminUnlinkIdentity(e.ctx, root, u.ID); err != nil {
		t.Fatalf("not linked: %v", err)
	}
}

// id без AUTOINCREMENT: наибольший номер достаётся следующей строке, и старый ключ
// не должен пройти по кешу только потому, что совпали id и key_rev.
func TestCachedKeyOfDeletedUser(t *testing.T) {
	for name, remove := range map[string]func(t *testing.T, e *env, username string){
		"service": func(t *testing.T, e *env, n string) {
			if err := e.svc.DeleteUser(e.ctx, n); err != nil {
				t.Fatal(err)
			}
		},
		"cli": func(t *testing.T, e *env, n string) {
			if err := e.cli(t).DeleteUser(e.ctx, n); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			mustUser(t, e, "bob", RoleUser)
			oldKey := e.device(t, "bob", "Kindle")
			p, err := e.svc.Authenticate(e.ctx, "bob", oldKey, client)
			if err != nil {
				t.Fatal(err)
			}
			remove(t, e, "bob")

			mustUser(t, e, "carol", RoleUser)
			newKey := e.device(t, "carol", "Kindle")
			before := e.svc.argonCalls.Load()
			if _, err := e.svc.Authenticate(e.ctx, "carol", oldKey, client); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("old key: %v", err)
			}
			if e.svc.argonCalls.Load() == before {
				t.Fatal("old key answered from cache")
			}
			q, err := e.svc.Authenticate(e.ctx, "carol", newKey, client)
			if err != nil {
				t.Fatalf("new key: %v", err)
			}
			if q.UserID != p.UserID || q.CredentialID != p.CredentialID || q.KeyRev != p.KeyRev {
				t.Fatalf("ids differ, test proves nothing: %+v vs %+v", p, q)
			}
		})
	}
}
