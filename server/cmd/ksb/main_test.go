package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
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
