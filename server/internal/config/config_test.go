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
	want := Config{Listen: ":8080", DataDir: "/data", PanelPasswordLogin: true}
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
		PanelPasswordLogin: true,
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

func TestLoadAdminVarsTogether(t *testing.T) {
	for set, missing := range map[string]string{"ADMIN_USER": "ADMIN_PASSWORD", "ADMIN_PASSWORD": "ADMIN_USER"} {
		_, err := Load(env(map[string]string{set: "value"}))
		if err == nil || !strings.Contains(err.Error(), missing+" is not") {
			t.Fatalf("only %s: %v", set, err)
		}
	}
}

// oidcEnv — минимальная конфигурация OIDC плюс extra.
func oidcEnv(extra map[string]string) func(string) string {
	m := map[string]string{
		"KSB_OIDC_ISSUER":    "https://id.example.com",
		"KSB_OIDC_CLIENT_ID": "ksb",
		"KSB_PUBLIC_URL":     "https://books.example.com",
	}
	for k, v := range extra {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return env(m)
}

func TestLoadOIDCDefaults(t *testing.T) {
	c, err := Load(oidcEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := &OIDC{
		Issuer: "https://id.example.com", ClientID: "ksb", Name: "SSO",
		Scopes:        []string{"openid", "profile", "email"},
		UsernameClaim: "preferred_username",
		PublicURL:     "https://books.example.com",
	}
	if !reflect.DeepEqual(c.OIDC, want) || !c.PanelPasswordLogin {
		t.Fatalf("got %+v, want %+v", c.OIDC, want)
	}
	if got := c.OIDC.RedirectURL(); got != "https://books.example.com/api/v1/auth/oidc/callback" {
		t.Fatalf("redirect URL %q", got)
	}
}

func TestLoadOIDCOverrides(t *testing.T) {
	c, err := Load(oidcEnv(map[string]string{
		"KSB_OIDC_CLIENT_SECRET":   "s3cret",
		"KSB_OIDC_NAME":            "Zitadel",
		"KSB_OIDC_SCOPES":          "profile  groups",
		"KSB_OIDC_USERNAME_CLAIM":  "email",
		"KSB_OIDC_AUTO_CREATE":     "true",
		"KSB_OIDC_ADMIN_CLAIM":     "groups",
		"KSB_OIDC_ADMIN_VALUE":     "ksb-admins",
		"KSB_PUBLIC_URL":           "http://192.168.1.5:8080/",
		"KSB_PANEL_PASSWORD_LOGIN": "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := &OIDC{
		Issuer: "https://id.example.com", ClientID: "ksb", ClientSecret: "s3cret", Name: "Zitadel",
		Scopes:        []string{"openid", "profile", "groups"},
		UsernameClaim: "email", AutoCreate: true, AdminClaim: "groups", AdminValue: "ksb-admins",
		PublicURL: "http://192.168.1.5:8080",
	}
	if !reflect.DeepEqual(c.OIDC, want) || c.PanelPasswordLogin {
		t.Fatalf("got %+v (password login %v), want %+v", c.OIDC, c.PanelPasswordLogin, want)
	}
}

func TestLoadOIDCIncomplete(t *testing.T) {
	for _, missing := range []string{"KSB_OIDC_ISSUER", "KSB_OIDC_CLIENT_ID", "KSB_PUBLIC_URL"} {
		_, err := Load(oidcEnv(map[string]string{missing: ""}))
		if err == nil || !strings.Contains(err.Error(), missing+" is not set") {
			t.Fatalf("without %s: %v", missing, err)
		}
	}
	// Другие переменные без обязательной тройки OIDC не включают.
	c, err := Load(env(map[string]string{"KSB_OIDC_NAME": "Zitadel"}))
	if err != nil || c.OIDC != nil {
		t.Fatalf("only name: %+v %v", c.OIDC, err)
	}
}

func TestLoadPublicURL(t *testing.T) {
	for _, v := range []string{
		"books.example.com",
		"/books",
		"ftp://books.example.com",
		"https://",
		"https://books.example.com/ksb",
		"https://books.example.com/?a=b",
		"https://books.example.com?",
		"https://books.example.com#top",
		"https://user:pass@books.example.com",
	} {
		_, err := Load(oidcEnv(map[string]string{"KSB_PUBLIC_URL": v}))
		if err == nil || !strings.Contains(err.Error(), "KSB_PUBLIC_URL") {
			t.Fatalf("%q: %v", v, err)
		}
	}
	_, err := Load(oidcEnv(map[string]string{"KSB_PUBLIC_URL": "https://example.com/ksb"}))
	if !strings.Contains(err.Error(), "must not have a path") {
		t.Fatalf("path: %v", err)
	}
	for in, want := range map[string]string{
		"https://books.example.com/":     "https://books.example.com",
		"https://Books.Example.com:443":  "https://books.example.com",
		"http://books.example.com:80/":   "http://books.example.com",
		"http://192.168.1.5:8080":        "http://192.168.1.5:8080",
		"https://books.example.com:8443": "https://books.example.com:8443",
		"https://[::1]:443":              "https://[::1]",
	} {
		c, err := Load(oidcEnv(map[string]string{"KSB_PUBLIC_URL": in}))
		if err != nil || c.OIDC.PublicURL != want {
			t.Fatalf("%q: %+v %v, want %q", in, c.OIDC, err, want)
		}
	}
}

func TestLoadOIDCAdminVarsTogether(t *testing.T) {
	for set, missing := range map[string]string{"KSB_OIDC_ADMIN_CLAIM": "KSB_OIDC_ADMIN_VALUE", "KSB_OIDC_ADMIN_VALUE": "KSB_OIDC_ADMIN_CLAIM"} {
		_, err := Load(oidcEnv(map[string]string{set: "value"}))
		if err == nil || !strings.Contains(err.Error(), missing+" is not") {
			t.Fatalf("only %s: %v", set, err)
		}
	}
}

func TestLoadPasswordLoginWithoutOIDC(t *testing.T) {
	_, err := Load(env(map[string]string{"KSB_PANEL_PASSWORD_LOGIN": "false"}))
	if err == nil || !strings.Contains(err.Error(), "requires OIDC") {
		t.Fatalf("error = %v", err)
	}
	if _, err := Load(oidcEnv(map[string]string{"KSB_PANEL_PASSWORD_LOGIN": "nope"})); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}
