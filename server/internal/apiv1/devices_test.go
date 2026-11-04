package apiv1

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
)

var client = netip.MustParseAddr("192.0.2.1")

var devicePassword = regexp.MustCompile(`^[a-z2-7]{4}-[a-z2-7]{4}$`)

// panelUser создаёт пользователя с паролем панели и входит им.
func (e *env) panelUser(name string, role accounts.Role) *browser {
	e.t.Helper()
	if _, err := e.acc.CreateUser(e.ctx, name, role); err != nil {
		e.t.Fatal(err)
	}
	if err := e.acc.SetPanelPassword(e.ctx, name, name+"-password", 0); err != nil {
		e.t.Fatal(err)
	}
	b := e.browser()
	b.login(name, name+"-password").expect(e.t, "login "+name, 200, "")
	return b
}

// created проверяет ответ создания или сброса и возвращает устройство и пароль.
func created(t *testing.T, r resp, status int) (map[string]any, string) {
	t.Helper()
	r.expect(t, "device with password", status, "")
	d, _ := r.body["device"].(map[string]any)
	pass, _ := r.body["password"].(string)
	if d == nil || !devicePassword.MatchString(pass) {
		t.Fatalf("body %v", r.body)
	}
	return d, pass
}

func path(d map[string]any, suffix string) string {
	return fmt.Sprintf("/api/v1/devices/%.0f%s", d["id"], suffix)
}

func (e *env) auth(user, pass string) error {
	_, err := e.acc.Authenticate(e.ctx, user, accounts.KeyFromPassword(pass), client)
	return err
}

func listNames(t *testing.T, b *browser) string {
	t.Helper()
	r := b.do("GET", "/api/v1/devices", nil)
	r.expect(t, "list", 200, "")
	var out []string
	for _, it := range r.items {
		d := it.(map[string]any)
		if _, ok := d["password"]; ok {
			t.Fatalf("password in list: %v", d)
		}
		s := d["owner"].(string) + "/" + d["name"].(string)
		if d["revoked_at"] != nil {
			s += "(revoked)"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestDevicesWithoutSession(t *testing.T) {
	b := newEnv(t).browser()
	b.do("GET", "/api/v1/devices", nil).expect(t, "list", 401, "unauthorized")
	b.do("POST", "/api/v1/devices", map[string]string{"name": "Kobo"}).expect(t, "create", 401, "unauthorized")
	b.do("PATCH", "/api/v1/devices/1", map[string]string{"name": "Kobo"}).expect(t, "rename", 401, "unauthorized")
	b.do("POST", "/api/v1/devices/1/password", nil).expect(t, "reset", 401, "unauthorized")
	b.do("DELETE", "/api/v1/devices/1", nil).expect(t, "revoke", 401, "unauthorized")
	b.do("GET", "/api/v1/users", nil).expect(t, "users", 401, "unauthorized")
}

func TestCreateDevice(t *testing.T) {
	e := newEnv(t)
	alice := e.panelUser("alice", accounts.RoleUser)
	admin := e.panelUser("root", accounts.RoleAdmin)
	if r := alice.do("GET", "/api/v1/devices", nil); r.status != 200 || r.items == nil || len(r.items) != 0 {
		t.Fatalf("empty list: %+v", r)
	}

	d, pass := created(t, alice.do("POST", "/api/v1/devices", map[string]string{"name": "  Kobo  "}), 201)
	if d["name"] != "Kobo" || d["owner"] != "alice" || d["revoked_at"] != nil || d["last_used_at"] != nil || d["reported_device"] != nil {
		t.Fatalf("device %v", d)
	}
	if err := e.auth("alice", pass); err != nil {
		t.Fatalf("new device key: %v", err)
	}
	created(t, alice.do("POST", "/api/v1/devices", map[string]string{"name": "Kindle", "username": "alice"}), 201)

	alice.do("POST", "/api/v1/devices", map[string]string{"name": "Kobo"}).expect(t, "taken", 409, "device_exists")
	alice.do("POST", "/api/v1/devices", map[string]string{"name": " "}).expect(t, "blank", 400, "invalid_device_name")
	alice.do("POST", "/api/v1/devices", map[string]string{"name": strings.Repeat("ж", 65)}).expect(t, "long", 400, "invalid_device_name")
	alice.do("POST", "/api/v1/devices", nil).expect(t, "no body", 400, "bad_request")
	alice.do("POST", "/api/v1/devices", map[string]string{"name": "Boox", "username": "root"}).expect(t, "other user", 403, "forbidden")

	ad, _ := created(t, admin.do("POST", "/api/v1/devices", map[string]string{"name": "Boox", "username": "alice"}), 201)
	if ad["owner"] != "alice" {
		t.Fatalf("admin-created device %v", ad)
	}
	admin.do("POST", "/api/v1/devices", map[string]string{"name": "Boox", "username": "ghost"}).expect(t, "unknown user", 404, "user_not_found")
	created(t, admin.do("POST", "/api/v1/devices", map[string]string{"name": "Kindle"}), 201)

	if got := listNames(t, alice); got != "alice/Kobo,alice/Kindle,alice/Boox" {
		t.Fatalf("alice sees %s", got)
	}
	if got := listNames(t, admin); got != "alice/Kobo,alice/Kindle,alice/Boox,root/Kindle" {
		t.Fatalf("admin sees %s", got)
	}
	if !strings.Contains(e.logs.String(), `"msg":"panel: device added","user":"root","owner":"alice"`) {
		t.Fatalf("admin action not logged:\n%s", e.logs)
	}
}

func TestDeviceLifecycle(t *testing.T) {
	e := newEnv(t)
	alice := e.panelUser("alice", accounts.RoleUser)
	bob := e.panelUser("bob", accounts.RoleUser)
	admin := e.panelUser("root", accounts.RoleAdmin)
	kbo, oldPass := created(t, alice.do("POST", "/api/v1/devices", map[string]string{"name": "Kbo"}), 201)
	created(t, alice.do("POST", "/api/v1/devices", map[string]string{"name": "Kindle"}), 201)
	p, err := e.acc.Authenticate(e.ctx, "alice", accounts.KeyFromPassword(oldPass), client)
	if err != nil {
		t.Fatal(err)
	}

	// Чужое устройство для bob не существует.
	bob.do("PATCH", path(kbo, ""), map[string]string{"name": "Mine"}).expect(t, "bob rename", 404, "not_found")
	bob.do("POST", path(kbo, "/password"), nil).expect(t, "bob reset", 404, "not_found")
	bob.do("DELETE", path(kbo, ""), nil).expect(t, "bob revoke", 404, "not_found")
	if got := listNames(t, bob); got != "" {
		t.Fatalf("bob sees %s", got)
	}
	if got := listNames(t, alice); got != "alice/Kbo,alice/Kindle" {
		t.Fatalf("after bob: %s", got)
	}
	alice.do("PATCH", "/api/v1/devices/abc", map[string]string{"name": "x"}).expect(t, "bad id", 400, "bad_request")

	// Что сообщает читалка.
	if err := progress.New(e.db).Save(e.ctx, p, progress.Update{
		Document: "0123456789abcdef0123456789abcdef", Percentage: 0.5, Progress: "1",
		Device: "PocketBook 740", ReceivedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, it := range alice.do("GET", "/api/v1/devices", nil).items {
		d := it.(map[string]any)
		want := map[string]any{"Kbo": "PocketBook 740", "Kindle": nil}[d["name"].(string)]
		if d["reported_device"] != want {
			t.Fatalf("reported_device of %v", d)
		}
	}

	// Переименование.
	r := alice.do("PATCH", path(kbo, ""), map[string]string{"name": " Kobo "})
	r.expect(t, "rename", 200, "")
	if r.body["name"] != "Kobo" || r.body["id"] != kbo["id"] {
		t.Fatalf("renamed %v", r.body)
	}
	if _, ok := r.body["password"]; ok {
		t.Fatal("password in rename response")
	}
	alice.do("PATCH", path(kbo, ""), map[string]string{"name": "Kindle"}).expect(t, "taken", 409, "device_exists")
	alice.do("PATCH", path(kbo, ""), map[string]string{"name": ""}).expect(t, "blank", 400, "invalid_device_name")
	if err := e.auth("alice", oldPass); err != nil {
		t.Fatalf("key after rename: %v", err)
	}

	// Новый пароль: закешированный старый ключ перестаёт действовать сразу.
	d, newPass := created(t, alice.do("POST", path(kbo, "/password"), nil), 200)
	if d["id"] != kbo["id"] || d["name"] != "Kobo" || newPass == oldPass {
		t.Fatalf("reset %v", d)
	}
	if err := e.auth("alice", oldPass); !errors.Is(err, accounts.ErrUnauthorized) {
		t.Fatalf("old key after reset: %v", err)
	}
	if err := e.auth("alice", newPass); err != nil {
		t.Fatalf("new key: %v", err)
	}

	// Администратор отзывает; повторный отзыв время не меняет.
	admin.do("DELETE", path(kbo, ""), nil).expect(t, "admin revoke", 204, "")
	if err := e.auth("alice", newPass); !errors.Is(err, accounts.ErrUnauthorized) {
		t.Fatalf("revoked key: %v", err)
	}
	revokedAt := func() any {
		for _, it := range alice.do("GET", "/api/v1/devices", nil).items {
			if d := it.(map[string]any); d["id"] == kbo["id"] {
				return d["revoked_at"]
			}
		}
		return nil
	}
	first := revokedAt()
	alice.do("DELETE", path(kbo, ""), nil).expect(t, "second revoke", 204, "")
	if first == nil || revokedAt() != first {
		t.Fatalf("revoked_at %v → %v", first, revokedAt())
	}
	alice.do("PATCH", path(kbo, ""), map[string]string{"name": "Old"}).expect(t, "rename revoked", 404, "not_found")
	alice.do("POST", path(kbo, "/password"), nil).expect(t, "reset revoked", 404, "not_found")
	alice.do("DELETE", "/api/v1/devices/999", nil).expect(t, "unknown", 404, "not_found")

	// Имя отозванного свободно.
	created(t, alice.do("POST", "/api/v1/devices", map[string]string{"name": "Kobo"}), 201)
	if got := listNames(t, alice); got != "alice/Kobo(revoked),alice/Kindle,alice/Kobo" {
		t.Fatalf("after revoke: %s", got)
	}
	if err := e.auth("alice", oldPass); err == nil {
		t.Fatal("old key works")
	}
}

func TestListUsers(t *testing.T) {
	e := newEnv(t)
	alice := e.panelUser("alice", accounts.RoleUser)
	admin := e.panelUser("root", accounts.RoleAdmin)
	if err := e.acc.DisableUser(e.ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	alice.do("GET", "/api/v1/users", nil).expect(t, "disabled user", 401, "unauthorized")
	bob := e.panelUser("bob", accounts.RoleUser)
	bob.do("GET", "/api/v1/users", nil).expect(t, "user", 403, "forbidden")

	r := admin.do("GET", "/api/v1/users", nil)
	r.expect(t, "admin", 200, "")
	var got []string
	for _, it := range r.items {
		u := it.(map[string]any)
		got = append(got, fmt.Sprint(u["username"], "/", u["role"], "/", u["disabled"]))
	}
	if strings.Join(got, ",") != "alice/user/true,bob/user/false,root/admin/false" {
		t.Fatalf("users %v", got)
	}
}
