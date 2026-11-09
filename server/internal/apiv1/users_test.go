package apiv1

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
)

var issuedPanelPassword = regexp.MustCompile(`^[a-z2-7]{4}(-[a-z2-7]{4}){3}$`)

func userPath(id int64, suffix string) string {
	return fmt.Sprintf("/api/v1/users/%d%s", id, suffix)
}

func (e *env) uid(name string) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Read.QueryRow(`SELECT id FROM users WHERE username = ?`, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func issued(t *testing.T, r resp, status int, username string) string {
	t.Helper()
	r.expect(t, "issued password", status, "")
	pass, _ := r.body["password"].(string)
	if r.body["username"] != username || !issuedPanelPassword.MatchString(pass) {
		t.Fatalf("body %v", r.body)
	}
	return pass
}

func userRow(t *testing.T, b *browser, name string) map[string]any {
	t.Helper()
	r := b.do("GET", "/api/v1/users", nil)
	r.expect(t, "users", 200, "")
	for _, it := range r.items {
		if u := it.(map[string]any); u["username"] == name {
			return u
		}
	}
	return nil
}

func TestUsersWithoutSession(t *testing.T) {
	b := newEnv(t).browser()
	b.do("POST", "/api/v1/users", map[string]string{"username": "x", "role": "user"}).expect(t, "create", 401, "unauthorized")
	b.do("PATCH", "/api/v1/users/1", map[string]bool{"disabled": true}).expect(t, "patch", 401, "unauthorized")
	b.do("DELETE", "/api/v1/users/1", nil).expect(t, "delete", 401, "unauthorized")
	b.do("POST", "/api/v1/users/1/password", nil).expect(t, "password", 401, "unauthorized")
	b.do("DELETE", "/api/v1/users/1/oidc", nil).expect(t, "oidc", 401, "unauthorized")
}

func TestUserActionsNeedAdmin(t *testing.T) {
	e := newEnv(t)
	alice := e.panelUser("alice", accounts.RoleUser)
	e.panelUser("bob", accounts.RoleUser)
	bob := e.uid("bob")
	alice.do("POST", "/api/v1/users", map[string]string{"username": "x", "role": "user"}).expect(t, "create", 403, "forbidden")
	alice.do("PATCH", userPath(bob, ""), map[string]bool{"disabled": true}).expect(t, "patch", 403, "forbidden")
	alice.do("DELETE", userPath(bob, ""), nil).expect(t, "delete", 403, "forbidden")
	alice.do("POST", userPath(bob, "/password"), nil).expect(t, "password", 403, "forbidden")
	alice.do("DELETE", userPath(bob, "/oidc"), nil).expect(t, "oidc", 403, "forbidden")
	alice.do("GET", "/api/v1/users", nil).expect(t, "list", 403, "forbidden")
	u, err := e.acc.UserByID(e.ctx, bob)
	if err != nil || u.DisabledAt != nil {
		t.Fatalf("bob changed: %+v %v", u, err)
	}
}

func TestUserActionsUnknownID(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	root.do("PATCH", userPath(999, ""), map[string]bool{"disabled": true}).expect(t, "patch", 404, "user_not_found")
	root.do("DELETE", userPath(999, ""), nil).expect(t, "delete", 404, "user_not_found")
	root.do("POST", userPath(999, "/password"), nil).expect(t, "password", 404, "user_not_found")
	root.do("DELETE", userPath(999, "/oidc"), nil).expect(t, "oidc", 404, "user_not_found")
	root.do("PATCH", "/api/v1/users/abc", map[string]bool{"disabled": true}).expect(t, "bad id", 400, "bad_request")
}

func TestUserSelfAction(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	self := e.uid("root")
	root.do("PATCH", userPath(self, ""), map[string]bool{"disabled": true}).expect(t, "disable", 409, "self_action")
	root.do("PATCH", userPath(self, ""), map[string]string{"role": "user"}).expect(t, "role", 409, "self_action")
	root.do("POST", userPath(self, "/password"), nil).expect(t, "password", 409, "self_action")
	root.do("DELETE", userPath(self, "/oidc"), nil).expect(t, "oidc", 409, "self_action")
	root.do("DELETE", userPath(self, ""), nil).expect(t, "delete", 409, "self_action")
	if u := userRow(t, root, "root"); u["disabled"] != false || u["role"] != "admin" {
		t.Fatalf("root %v", u)
	}
	root.login("root", "root-password").expect(t, "password unchanged", 200, "")
}

func TestCreateUserAPI(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	pass := issued(t, root.do("POST", "/api/v1/users", map[string]string{"username": "carol", "role": "user"}), 201, "carol")

	carol := e.browser()
	carol.login("carol", pass).expect(t, "login", 200, "")
	if r := carol.do("GET", "/api/v1/devices", nil); r.status != 200 || len(r.items) != 0 {
		t.Fatalf("carol devices: %+v", r)
	}

	root.do("POST", "/api/v1/users", map[string]string{"username": "carol", "role": "admin"}).expect(t, "taken", 409, "user_exists")
	if u := userRow(t, root, "carol"); u["role"] != "user" {
		t.Fatalf("existing user changed: %v", u)
	}
	root.do("POST", "/api/v1/users", map[string]string{"username": "a:b", "role": "user"}).expect(t, "bad name", 400, "invalid_username")
	root.do("POST", "/api/v1/users", map[string]string{"username": "dan", "role": "root"}).expect(t, "bad role", 400, "bad_request")
	root.do("POST", "/api/v1/users", map[string]string{"username": "dan"}).expect(t, "no role", 400, "bad_request")

	logs := e.logs.String()
	if !strings.Contains(logs, `"msg":"panel: user created","user":"root","target":"carol"`) {
		t.Fatalf("not logged:\n%s", logs)
	}
	if strings.Contains(logs, pass) {
		t.Fatalf("password in log:\n%s", logs)
	}
}

func TestResetUserPasswordAPI(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	bob := e.panelUser("bob", accounts.RoleUser)
	_, devicePass, err := e.acc.AddDevice(e.ctx, "bob", "Kindle")
	if err != nil {
		t.Fatal(err)
	}
	pass := issued(t, root.do("POST", userPath(e.uid("bob"), "/password"), nil), 200, "bob")
	bob.do("GET", "/api/v1/me", nil).expect(t, "old session", 401, "unauthorized")
	e.browser().login("bob", "bob-password").expect(t, "old password", 401, "invalid_credentials")
	e.browser().login("bob", pass).expect(t, "new password", 200, "")
	if err := e.auth("bob", devicePass); err != nil {
		t.Fatalf("device key: %v", err)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, `"msg":"panel: user password reset","user":"root","target":"bob"`) || strings.Contains(logs, pass) {
		t.Fatalf("log:\n%s", logs)
	}
}

func TestResetUserPasswordLoginDisabled(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	u, err := e.acc.CreateOIDCUser(e.ctx, "bob", accounts.RoleUser, "https://id.example.com", "1")
	if err != nil {
		t.Fatal(err)
	}
	e.panelUser("carol", accounts.RoleUser)
	e.srv.PasswordLoginUnlinkedOnly = true

	root.do("POST", userPath(u.ID, "/password"), nil).expect(t, "linked", 403, "password_login_disabled")
	if row := userRow(t, root, "bob"); row["has_password"] != false {
		t.Fatalf("password set: %v", row)
	}
	// Непривязанному пароль работает.
	issued(t, root.do("POST", userPath(e.uid("carol"), "/password"), nil), 200, "carol")

	// Своя строка — self_action даже у привязанного администратора.
	if err := e.acc.LinkIdentity(e.ctx, e.uid("root"), "https://id.example.com", "2"); err != nil {
		t.Fatal(err)
	}
	root.do("POST", userPath(e.uid("root"), "/password"), nil).expect(t, "self", 409, "self_action")
}

func TestUpdateUserAPI(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	bob := e.panelUser("bob", accounts.RoleUser)
	id := e.uid("bob")

	root.do("PATCH", userPath(id, ""), map[string]any{}).expect(t, "empty", 400, "bad_request")
	root.do("PATCH", userPath(id, ""), map[string]string{"role": "root"}).expect(t, "bad role", 400, "bad_request")

	bob.do("GET", "/api/v1/users", nil).expect(t, "before role", 403, "forbidden")
	root.do("PATCH", userPath(id, ""), map[string]string{"role": "admin"}).expect(t, "role", 204, "")
	bob.do("GET", "/api/v1/users", nil).expect(t, "after role", 200, "")

	// Уже включён: роль применяется, сессия жива.
	root.do("PATCH", userPath(id, ""), map[string]any{"disabled": false, "role": "user"}).expect(t, "enable enabled", 204, "")
	bob.do("GET", "/api/v1/users", nil).expect(t, "session alive, role back", 403, "forbidden")

	root.do("PATCH", userPath(id, ""), map[string]bool{"disabled": true}).expect(t, "disable", 204, "")
	bob.do("GET", "/api/v1/me", nil).expect(t, "disabled session", 401, "unauthorized")
	if u := userRow(t, root, "bob"); u["disabled"] != true {
		t.Fatalf("bob %v", u)
	}
	root.do("PATCH", userPath(id, ""), map[string]bool{"disabled": false}).expect(t, "enable", 204, "")
	bob.do("GET", "/api/v1/me", nil).expect(t, "old session", 401, "unauthorized")
	e.browser().login("bob", "bob-password").expect(t, "login again", 200, "")
	if !strings.Contains(e.logs.String(), `"msg":"panel: user updated","user":"root","target":"bob"`) {
		t.Fatalf("not logged:\n%s", e.logs)
	}
}

func TestEnableReturnsRejectedDevice(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	e.panelUser("bob", accounts.RoleUser)
	_, devicePass, err := e.acc.AddDevice(e.ctx, "bob", "Kindle")
	if err != nil {
		t.Fatal(err)
	}
	id := e.uid("bob")
	root.do("PATCH", userPath(id, ""), map[string]bool{"disabled": true}).expect(t, "disable", 204, "")
	if err := e.auth("bob", devicePass); !errors.Is(err, accounts.ErrUnauthorized) {
		t.Fatalf("disabled: %v", err)
	}
	if err := e.auth("bob", devicePass); !errors.Is(err, accounts.ErrRepeated) {
		t.Fatalf("not remembered: %v", err)
	}
	root.do("PATCH", userPath(id, ""), map[string]bool{"disabled": false}).expect(t, "enable", 204, "")
	if err := e.auth("bob", devicePass); err != nil {
		t.Fatalf("after enable: %v", err)
	}
}

func TestDemotedActorCannotAct(t *testing.T) {
	e := newEnv(t)
	alice := e.panelUser("alice", accounts.RoleAdmin)
	bob := e.panelUser("bob", accounts.RoleAdmin)
	bob.do("PATCH", userPath(e.uid("alice"), ""), map[string]string{"role": "user"}).expect(t, "first", 204, "")
	alice.do("PATCH", userPath(e.uid("bob"), ""), map[string]string{"role": "user"}).expect(t, "second", 403, "forbidden")
	if u := userRow(t, bob, "bob"); u["role"] != "admin" {
		t.Fatalf("bob %v", u)
	}
}

func TestUnlinkUserOIDCAPI(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	u, err := e.acc.CreateOIDCUser(e.ctx, "bob", accounts.RoleUser, "https://id.example.com", "1")
	if err != nil {
		t.Fatal(err)
	}
	root.do("DELETE", userPath(u.ID, "/oidc"), nil).expect(t, "unlink", 204, "")
	if _, err := e.acc.UserByIdentity(e.ctx, "https://id.example.com", "1"); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("still linked: %v", err)
	}
	root.do("DELETE", userPath(u.ID, "/oidc"), nil).expect(t, "not linked", 204, "")
}

func TestDeleteUserAPI(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	bob := e.panelUser("bob", accounts.RoleUser)
	_, devicePass, err := e.acc.AddDevice(e.ctx, "bob", "Kindle")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.auth("bob", devicePass); err != nil {
		t.Fatal(err)
	}
	root.do("DELETE", userPath(e.uid("bob"), ""), nil).expect(t, "delete", 204, "")
	if u := userRow(t, root, "bob"); u != nil {
		t.Fatalf("still listed: %v", u)
	}
	bob.do("GET", "/api/v1/me", nil).expect(t, "session", 401, "unauthorized")
	if err := e.auth("bob", devicePass); !errors.Is(err, accounts.ErrUnauthorized) {
		t.Fatalf("deleted key: %v", err)
	}
	if !strings.Contains(e.logs.String(), `"msg":"panel: user deleted","user":"root","target":"bob"`) {
		t.Fatalf("not logged:\n%s", e.logs)
	}
}

func TestListUsersFields(t *testing.T) {
	e := newEnv(t)
	root := e.panelUser("root", accounts.RoleAdmin)
	if _, err := e.acc.CreateUser(e.ctx, "bob", accounts.RoleUser); err != nil {
		t.Fatal(err)
	}
	if _, err := e.acc.CreateOIDCUser(e.ctx, "carol", accounts.RoleUser, "https://id.example.com", "1"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Kindle", "Kobo"} {
		if _, _, err := e.acc.AddDevice(e.ctx, "bob", name); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.acc.RevokeDevice(e.ctx, "bob", "Kobo"); err != nil {
		t.Fatal(err)
	}

	b := userRow(t, root, "bob")
	if b["id"] != float64(e.uid("bob")) || b["device_count"] != float64(1) || b["has_password"] != false ||
		b["oidc_linked"] != false {
		t.Fatalf("bob %v", b)
	}
	if v, ok := b["last_active_at"]; !ok || v != nil {
		t.Fatalf("last_active_at must be null, got %v (%v)", v, ok)
	}
	if _, err := time.Parse(time.RFC3339, b["created_at"].(string)); err != nil {
		t.Fatalf("created_at: %v", err)
	}
	if c := userRow(t, root, "carol"); c["oidc_linked"] != true {
		t.Fatalf("carol %v", c)
	}
	r := userRow(t, root, "root")
	if r["has_password"] != true || r["last_active_at"] == nil {
		t.Fatalf("root %v", r)
	}
}
