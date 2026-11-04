package main

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/api/v1/health",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/api/v1/health",
		"[::]:9000":      "http://127.0.0.1:9000/api/v1/health",
		"10.0.0.5:8080":  "http://10.0.0.5:8080/api/v1/health",
		"localhost:1234": "http://localhost:1234/api/v1/health",
	} {
		if got := healthURL(in); got != want {
			t.Errorf("healthURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCLI(t *testing.T) {
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	ksb := func(args ...string) (string, int) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb)
		return out.String() + errb.String(), code
	}
	if out, code := ksb("user", "add", "alice", "-role", "admin"); code != 0 {
		t.Fatalf("user add: %d %s", code, out)
	}
	if out, code := ksb("user", "add", "alice"); code == 0 {
		t.Fatalf("duplicate user add succeeded: %s", out)
	}
	out, code := ksb("device", "add", "alice", "Kindle")
	if code != 0 || !regexp.MustCompile(`(?m)^  password: [a-z2-7]{4}-[a-z2-7]{4}$`).MatchString(out) {
		t.Fatalf("device add: %d %s", code, out)
	}
	if out, _ := ksb("device", "list", "alice"); !strings.Contains(out, "Kindle") || strings.Contains(out, "password") {
		t.Fatalf("device list: %s", out)
	}
	if out, code := ksb("device", "revoke", "alice", "Kindle"); code != 0 {
		t.Fatalf("device revoke: %s", out)
	}
	if out, code := ksb("device", "add", "alice", "  Kobo  "); code != 0 || !strings.Contains(out, `device "Kobo" added`) {
		t.Fatalf("device add with spaces: %d %s", code, out)
	}
	if out, code := ksb("device", "revoke", "alice", " Kobo"); code != 0 {
		t.Fatalf("device revoke with spaces: %s", out)
	}
	if out, code := ksb("device", "add", "alice", strings.Repeat("ж", 65)); code != 1 || !strings.Contains(out, "device name") {
		t.Fatalf("long device name: %d %s", code, out)
	}
	if out, _ := ksb("user", "list"); !strings.Contains(out, "alice") || !strings.Contains(out, "admin") {
		t.Fatalf("user list: %s", out)
	}
	if _, code := ksb("user", "disable", "alice"); code != 0 {
		t.Fatal("disable failed")
	}
	if _, code := ksb("user", "delete", "alice"); code != 0 {
		t.Fatal("delete failed")
	}
	if _, code := ksb("user", "delete", "alice"); code != 1 {
		t.Fatal("second delete should fail")
	}
	if _, code := ksb("bogus"); code != 2 {
		t.Fatal("unknown command should exit 2")
	}
}

func TestUserPasswd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KSB_DATA_DIR", dir)
	ksb := func(args ...string) (string, int) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb)
		return out.String() + errb.String(), code
	}
	if out, code := ksb("user", "add", "alice"); code != 0 {
		t.Fatal(out)
	}
	out, code := ksb("device", "add", "alice", "Kindle")
	device := regexp.MustCompile(`(?m)^  password: (\S+)$`).FindStringSubmatch(out)
	if code != 0 || device == nil {
		t.Fatalf("device add: %s", out)
	}

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(dir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	acc := accounts.New(db)
	var id int64
	if err := db.Read.QueryRow(`SELECT id FROM users WHERE username = 'alice'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	token, _, err := acc.CreateSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	out, code = ksb("user", "passwd", "alice")
	m := regexp.MustCompile(`(?m)^  password: ([a-z2-7]{4}(?:-[a-z2-7]{4}){3})$`).FindStringSubmatch(out)
	if code != 0 || m == nil {
		t.Fatalf("user passwd: %d %s", code, out)
	}
	client := netip.MustParseAddr("192.0.2.1")
	if _, err := acc.VerifyPanelPassword(ctx, "alice", m[1], client); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
	if _, _, err := acc.SessionByToken(ctx, token); !errors.Is(err, accounts.ErrUnauthorized) {
		t.Fatalf("session survived passwd: %v", err)
	}
	if _, err := acc.Authenticate(ctx, "alice", accounts.KeyFromPassword(device[1]), client); err != nil {
		t.Fatalf("device key changed: %v", err)
	}
	if out, code := ksb("user", "passwd", "ghost"); code != 1 || !strings.Contains(out, "not found") {
		t.Fatalf("unknown user: %d %s", code, out)
	}
}
