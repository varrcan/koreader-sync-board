package accounts

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeDeviceName(t *testing.T) {
	for in, want := range map[string]string{
		"  Kobo  ":                    "Kobo",
		"\tPocketBook 740\n":          "PocketBook 740",
		strings.Repeat("ж", 64):       strings.Repeat("ж", 64),
		" " + strings.Repeat("ж", 64): strings.Repeat("ж", 64),
	} {
		got, err := normalizeDeviceName(in)
		if err != nil || got != want {
			t.Errorf("normalizeDeviceName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("ж", 65)} {
		if _, err := normalizeDeviceName(bad); !errors.Is(err, ErrInvalidDeviceName) || !errors.Is(err, ErrInvalid) {
			t.Errorf("normalizeDeviceName(%q): %v", bad, err)
		}
	}
}

func TestAddDeviceName(t *testing.T) {
	e := newEnv(t)
	mustUser(t, e, "alice", RoleUser)
	d, _, err := e.svc.AddDevice(e.ctx, "alice", "  Kobo  ")
	if err != nil || d.Name != "Kobo" || d.Owner != "alice" {
		t.Fatalf("AddDevice: %+v %v", d, err)
	}
	if _, _, err := e.svc.AddDevice(e.ctx, "alice", "Kobo "); !errors.Is(err, ErrDeviceExists) {
		t.Fatalf("trimmed duplicate: %v", err)
	}
	for _, bad := range []string{" ", strings.Repeat("ж", 65)} {
		if _, _, err := e.svc.AddDevice(e.ctx, "alice", bad); !errors.Is(err, ErrInvalidDeviceName) {
			t.Fatalf("AddDevice(%q): %v", bad, err)
		}
	}
	// CLI отзывает по имени, введённому с пробелами.
	if err := e.svc.RevokeDevice(e.ctx, "alice", " Kobo"); err != nil {
		t.Fatal(err)
	}
	// Имя из старой базы, сохранённое без обрезки, отзывается как есть.
	if _, err := e.db.Write.ExecContext(e.ctx, `
		INSERT INTO device_credentials (user_id, name, key_hash, created_at)
		SELECT id, ' Old Kobo ', 'x', 0 FROM users WHERE username = 'alice'`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeDevice(e.ctx, "alice", " Old Kobo "); err != nil {
		t.Fatalf("legacy untrimmed name: %v", err)
	}
	if err := e.svc.RevokeDevice(e.ctx, "alice", " Kobo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
}

// devices — пользователи alice (user) и root (admin) с устройствами.
type devices struct {
	*env
	alice, root  Scope
	kindle, kobo DeviceInfo
	kindleKey    string
	rootDev      DeviceInfo
}

func newDevices(t *testing.T) devices {
	t.Helper()
	e := newEnv(t)
	alice, err := e.svc.CreateUser(e.ctx, "alice", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	root, err := e.svc.CreateUser(e.ctx, "root", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	add := func(id int64, name string) (DeviceInfo, string) {
		d, pass, err := e.svc.AddDeviceFor(e.ctx, id, name)
		if err != nil {
			t.Fatal(err)
		}
		return d, md5hex(pass)
	}
	kindle, kindleKey := add(alice.ID, "Kindle")
	kobo, _ := add(alice.ID, "Kobo")
	rootDev, _ := add(root.ID, "Boox")
	return devices{
		env: e, alice: Scope{UserID: alice.ID}, root: Scope{UserID: root.ID, All: true},
		kindle: kindle, kobo: kobo, kindleKey: kindleKey, rootDev: rootDev,
	}
}

// event пишет событие прогресса от устройства прямо в журнал.
func (e *env) event(t *testing.T, d DeviceInfo, device string) {
	t.Helper()
	if _, err := e.db.Write.ExecContext(e.ctx,
		`INSERT INTO documents (hash, first_seen_at) VALUES ('doc', 0) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Write.ExecContext(e.ctx, `
		INSERT INTO progress_events (user_id, document_id, credential_id, percentage, progress, device, server_ts)
		SELECT user_id, (SELECT id FROM documents WHERE hash = 'doc'), id, 0.5, '1', ?, 0
		FROM device_credentials WHERE id = ?`, device, d.ID); err != nil {
		t.Fatal(err)
	}
}

func names(ds []DeviceInfo) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Owner+"/"+d.Name)
	}
	return strings.Join(out, ",")
}

func TestDevicesScope(t *testing.T) {
	e := newDevices(t)
	if err := e.svc.RevokeDeviceByID(e.ctx, e.alice, e.kobo.ID); err != nil {
		t.Fatal(err)
	}
	e.event(t, e.kindle, "PocketBook 629")
	e.event(t, e.kindle, "PocketBook 740")

	own, err := e.svc.Devices(e.ctx, e.alice)
	if err != nil {
		t.Fatal(err)
	}
	if names(own) != "alice/Kindle,alice/Kobo" {
		t.Fatalf("alice sees %s", names(own))
	}
	if own[1].RevokedAt == nil || own[0].RevokedAt != nil {
		t.Fatalf("revoked_at: %+v", own)
	}
	if r := own[0].ReportedDevice; r == nil || *r != "PocketBook 740" {
		t.Fatalf("reported_device = %v", r)
	}
	if own[1].ReportedDevice != nil {
		t.Fatalf("device without events reports %q", *own[1].ReportedDevice)
	}

	all, err := e.svc.Devices(e.ctx, e.root)
	if err != nil {
		t.Fatal(err)
	}
	if names(all) != "alice/Kindle,alice/Kobo,root/Boox" {
		t.Fatalf("admin sees %s", names(all))
	}
	// Без All администратор видит только свои.
	if own, _ := e.svc.Devices(e.ctx, Scope{UserID: e.root.UserID}); names(own) != "root/Boox" {
		t.Fatalf("admin without All sees %s", names(own))
	}
}

func TestRenameDeviceByID(t *testing.T) {
	e := newDevices(t)
	d, err := e.svc.RenameDeviceByID(e.ctx, e.alice, e.kindle.ID, " Kindle Paperwhite ")
	if err != nil || d.Name != "Kindle Paperwhite" || d.ID != e.kindle.ID {
		t.Fatalf("rename: %+v %v", d, err)
	}
	if _, err := e.svc.RenameDeviceByID(e.ctx, e.alice, e.kindle.ID, "Kindle Paperwhite"); err != nil {
		t.Fatalf("same name: %v", err)
	}
	if _, err := e.svc.RenameDeviceByID(e.ctx, e.alice, e.kindle.ID, "Kobo"); !errors.Is(err, ErrDeviceExists) {
		t.Fatalf("taken name: %v", err)
	}
	if _, err := e.svc.RenameDeviceByID(e.ctx, e.alice, e.kindle.ID, ""); !errors.Is(err, ErrInvalidDeviceName) {
		t.Fatalf("empty name: %v", err)
	}
	// Имя устройства другого пользователя не мешает.
	if _, err := e.svc.RenameDeviceByID(e.ctx, e.alice, e.kindle.ID, "Boox"); err != nil {
		t.Fatalf("name of other user's device: %v", err)
	}
	if err := e.svc.RevokeDeviceByID(e.ctx, e.alice, e.kobo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RenameDeviceByID(e.ctx, e.alice, e.kindle.ID, "Kobo"); err != nil {
		t.Fatalf("name of revoked device: %v", err)
	}
	if _, err := e.svc.RenameDeviceByID(e.ctx, e.alice, e.kobo.ID, "Old Kobo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename revoked: %v", err)
	}
	if _, err := e.svc.RenameDeviceByID(e.ctx, e.alice, e.rootDev.ID, "Mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename other user's device: %v", err)
	}
	if _, err := e.svc.RenameDeviceByID(e.ctx, e.root, e.kindle.ID, "Kindle"); err != nil {
		t.Fatalf("admin rename: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", e.kindleKey, client); err != nil {
		t.Fatalf("key after rename: %v", err)
	}
}

func TestResetDeviceKey(t *testing.T) {
	e := newDevices(t)
	before, err := e.svc.Authenticate(e.ctx, "alice", e.kindleKey, client)
	if err != nil {
		t.Fatal(err)
	}
	d, pass, err := e.svc.ResetDeviceKey(e.ctx, e.alice, e.kindle.ID)
	if err != nil || d.ID != e.kindle.ID || d.Name != "Kindle" || pass == "" {
		t.Fatalf("reset: %+v %q %v", d, pass, err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", e.kindleKey, client); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old cached key: %v", err)
	}
	p, err := e.svc.Authenticate(e.ctx, "alice", md5hex(pass), client)
	if err != nil || p.CredentialID != before.CredentialID || p.KeyRev != before.KeyRev+1 {
		t.Fatalf("new key: %+v %v", p, err)
	}

	if _, _, err := e.svc.ResetDeviceKey(e.ctx, e.alice, e.rootDev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's device: %v", err)
	}
	if err := e.svc.RevokeDeviceByID(e.ctx, e.alice, e.kobo.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResetDeviceKey(e.ctx, e.root, e.kobo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked device: %v", err)
	}
}

func TestRevokeDeviceByID(t *testing.T) {
	e := newDevices(t)
	if _, err := e.svc.Authenticate(e.ctx, "alice", e.kindleKey, client); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeDeviceByID(e.ctx, e.alice, e.rootDev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's device: %v", err)
	}
	if err := e.svc.RevokeDeviceByID(e.ctx, e.root, e.kindle.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", e.kindleKey, client); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked cached key: %v", err)
	}
	first, _ := e.svc.deviceByID(e.ctx, e.root, e.kindle.ID)
	e.clock.t = e.clock.t.Add(time.Hour)
	if err := e.svc.RevokeDeviceByID(e.ctx, e.alice, e.kindle.ID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	second, _ := e.svc.deviceByID(e.ctx, e.root, e.kindle.ID)
	if first.RevokedAt == nil || !second.RevokedAt.Equal(*first.RevokedAt) {
		t.Fatalf("revoked_at changed: %v → %v", first.RevokedAt, second.RevokedAt)
	}
	if err := e.svc.RevokeDeviceByID(e.ctx, e.root, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestAddDeviceFor(t *testing.T) {
	e := newDevices(t)
	if e.kindle.Owner != "alice" || e.rootDev.Owner != "root" || e.kindle.ReportedDevice != nil {
		t.Fatalf("created: %+v %+v", e.kindle, e.rootDev)
	}
	if _, _, err := e.svc.AddDeviceFor(e.ctx, e.alice.UserID, "Kindle"); !errors.Is(err, ErrDeviceExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, _, err := e.svc.AddDeviceFor(e.ctx, 999, "Kindle"); !errors.Is(err, ErrUserNotFound) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	if err := e.svc.RevokeDeviceByID(e.ctx, e.alice, e.kindle.ID); err != nil {
		t.Fatal(err)
	}
	d, pass, err := e.svc.AddDeviceFor(e.ctx, e.alice.UserID, "Kindle")
	if err != nil || d.ID == e.kindle.ID {
		t.Fatalf("name of revoked device: %+v %v", d, err)
	}
	if _, err := e.svc.Authenticate(e.ctx, "alice", md5hex(pass), client); err != nil {
		t.Fatal(err)
	}
}
