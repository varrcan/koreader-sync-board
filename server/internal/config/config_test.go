package config

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Listen: ":8080", DataDir: "/data"}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"KSB_LISTEN":              "127.0.0.1:9000",
		"KSB_DATA_DIR":            "/tmp/ksb",
		"KSB_KOSYNC_REGISTRATION": "true",
		"KSB_KOSYNC_SELF_DELETE":  "1",
		"KSB_TRUSTED_PROXIES":     "10.0.0.0/8, 192.168.1.10",
		"ADMIN_USER":              "root",
		"ADMIN_PASSWORD":          "secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen: "127.0.0.1:9000", DataDir: "/tmp/ksb",
		KosyncRegistration: true, KosyncSelfDelete: true,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.10/32")},
		AdminUser:      "root", AdminPassword: "secret",
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadInvalidBool(t *testing.T) {
	if _, err := Load(env(map[string]string{"KSB_KOSYNC_REGISTRATION": "yes please"})); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadInvalidTrustedProxies(t *testing.T) {
	_, err := Load(env(map[string]string{"KSB_TRUSTED_PROXIES": "10.0.0.0/8, proxy.lan"}))
	if err == nil || !strings.Contains(err.Error(), "KSB_TRUSTED_PROXIES") || !strings.Contains(err.Error(), `"proxy.lan"`) {
		t.Fatalf("error = %v", err)
	}
}
