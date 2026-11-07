package sso

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/config"
	"github.com/varrcan/koreader-sync-board/server/internal/sso/ssotest"
)

const publicURL = "https://books.example.com"

func newProvider(t *testing.T, mod func(*config.OIDC)) (*Provider, *ssotest.IdP) {
	t.Helper()
	idp := ssotest.New(t, "ksb")
	cfg := &config.OIDC{
		Issuer: idp.URL, ClientID: "ksb", Name: "Test",
		Scopes: []string{"openid", "profile"}, UsernameClaim: "preferred_username",
		PublicURL: publicURL,
	}
	if mod != nil {
		mod(cfg)
	}
	return New(cfg), idp
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// login проходит поток до callback: Start, /authorize у провайдера и запрос
// callback с cookie потока. mod может подменить callback до Finish.
func login(t *testing.T, p *Provider, mod func(*http.Request)) (Identity, Flow, error) {
	t.Helper()
	authURL, cookie, err := p.Start(context.Background(), "/books", false, true)
	if err != nil {
		t.Fatal(err)
	}
	res, err := noRedirect.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc := res.Header.Get("Location")
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(loc, publicURL+CallbackPath+"?") {
		t.Fatalf("authorize: %d %q", res.StatusCode, loc)
	}
	r := httptest.NewRequest("GET", loc, nil)
	r.AddCookie(cookie)
	if mod != nil {
		mod(r)
	}
	return p.Finish(r)
}

func TestClaimMatches(t *testing.T) {
	var zitadel any
	_ = json.Unmarshal([]byte(`{"admin": {"1": "example.com"}, "reader": {"1": "example.com"}}`), &zitadel)
	for _, c := range []struct {
		name  string
		claim any
		want  bool
	}{
		{"string", "admin", true},
		{"other string", "user", false},
		{"array of strings", []any{"users", "admin"}, true},
		{"array without value", []any{"users"}, false},
		{"array of objects", []any{map[string]any{"admin": "x"}}, true},
		{"zitadel object", zitadel, true},
		{"object without key", map[string]any{"user": true}, false},
		{"number", 1.0, false},
		{"bool", true, false},
		{"null", nil, false},
	} {
		if got := claimMatches(c.claim, "admin"); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSafeRedirect(t *testing.T) {
	for in, want := range map[string]string{
		"/statistics":       "/statistics",
		"/account?oidc=x":   "/account?oidc=x",
		"":                  "/",
		"//evil.example":    "/",
		`/\evil.example`:    "/",
		"https://evil.test": "/",
		"/\t/evil.example":  "/",
		"books":             "/",
	} {
		if got := SafeRedirect(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestLazyDiscovery(t *testing.T) {
	p, idp := newProvider(t, nil)
	if idp.Discoveries() != 0 {
		t.Fatal("discovery at construction")
	}
	idp.Down(true)
	if _, _, err := p.Start(context.Background(), "/", false, false); !errors.Is(err, ErrUnavailable) || Code(err) != "oidc_unavailable" {
		t.Fatalf("down: %v", err)
	}
	idp.Down(false)
	if _, _, err := p.Start(context.Background(), "/", false, false); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
	// Провайдер создан один раз и дальше discovery не нужен.
	idp.Down(true)
	if _, _, err := p.Start(context.Background(), "/", false, false); err != nil {
		t.Fatalf("cached provider: %v", err)
	}
	if n := idp.Discoveries(); n != 1 {
		t.Fatalf("%d discoveries, want 1", n)
	}
}

func TestDiscoveryTimeout(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hang.Close)
	p := New(&config.OIDC{Issuer: hang.URL, ClientID: "ksb", PublicURL: publicURL})
	if p.client.Timeout != httpTimeout {
		t.Fatalf("client timeout %v", p.client.Timeout)
	}
	p.client.Timeout = 100 * time.Millisecond
	start := time.Now()
	if _, _, err := p.Start(context.Background(), "/", false, false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("hanging IdP: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestStart(t *testing.T) {
	p, idp := newProvider(t, nil)
	authURL, c, err := p.Start(context.Background(), "//evil.example", true, true)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if !strings.HasPrefix(authURL, idp.URL+"/authorize?") || q.Get("response_type") != "code" ||
		q.Get("client_id") != "ksb" || q.Get("redirect_uri") != publicURL+CallbackPath ||
		q.Get("scope") != "openid profile" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || len(q.Get("state")) < 22 || len(q.Get("nonce")) < 22 {
		t.Fatalf("auth URL %s", authURL)
	}
	if c.Name != CookieName || c.Path != CallbackPath || c.MaxAge != 600 || !c.HttpOnly || !c.Secure ||
		c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %+v", c)
	}
	r := httptest.NewRequest("GET", CallbackPath, nil)
	r.AddCookie(c)
	st, err := readCookie(r)
	if err != nil || st.Redirect != "/" || !st.Link || st.State != q.Get("state") || st.Nonce != q.Get("nonce") {
		t.Fatalf("cookie state %+v %v", st, err)
	}
	// Без HTTPS cookie не Secure, иначе панель не работала бы по http:// в LAN.
	if _, c, _ := p.Start(context.Background(), "/", false, false); c.Secure {
		t.Fatal("secure cookie without HTTPS")
	}
}

func TestOnPublicHost(t *testing.T) {
	p, _ := newProvider(t, nil)
	for host, want := range map[string]bool{
		"books.example.com":    true,
		"BOOKS.example.com":    true,
		"192.168.1.5:8080":     false,
		"books.example.com:81": false,
	} {
		r := httptest.NewRequest("GET", "http://"+host+"/api/v1/auth/oidc/login?redirect=/books", nil)
		if got := p.OnPublicHost(r); got != want {
			t.Errorf("%s: %v", host, got)
		}
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:8080/", nil)
	r.Header.Set("X-Forwarded-Host", "books.example.com, proxy.lan")
	if !p.OnPublicHost(r) {
		t.Error("X-Forwarded-Host ignored")
	}
	r = httptest.NewRequest("GET", "http://192.168.1.5:8080/api/v1/auth/oidc/login?redirect=/books", nil)
	if got := p.PublicURL(r); got != publicURL+"/api/v1/auth/oidc/login?redirect=/books" {
		t.Fatalf("public URL %q", got)
	}
}

func TestFinish(t *testing.T) {
	p, idp := newProvider(t, func(c *config.OIDC) { c.AdminClaim, c.AdminValue = "groups", "ksb-admins" })
	idp.SetClaims(map[string]any{"sub": "123", "preferred_username": "alice", "groups": []any{"ksb-admins"}})
	id, flow, err := login(t, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id.Issuer != idp.URL || id.Subject != "123" || id.Username != "alice" || id.Admin == nil || !*id.Admin {
		t.Fatalf("identity %+v", id)
	}
	if flow.Redirect != "/books" || flow.Link {
		t.Fatalf("flow %+v", flow)
	}

	idp.SetClaims(map[string]any{"sub": "123", "preferred_username": 42})
	id, _, err = login(t, p, nil)
	if err != nil || id.Username != "" || id.Admin == nil || *id.Admin {
		t.Fatalf("non-string name, no groups: %+v %v", id, err)
	}
}

func TestFinishWithoutRoleClaim(t *testing.T) {
	p, idp := newProvider(t, nil)
	idp.SetClaims(map[string]any{"sub": "123", "groups": []any{"ksb-admins"}})
	if id, _, err := login(t, p, nil); err != nil || id.Admin != nil {
		t.Fatalf("%+v %v", id, err)
	}
}

func TestFinishFailures(t *testing.T) {
	p, idp := newProvider(t, nil)
	idp.SetClaims(map[string]any{"sub": "123"})
	expectCode := func(name, code string, mod func(*http.Request)) {
		t.Helper()
		_, _, err := login(t, p, mod)
		if err == nil || Code(err) != code {
			t.Fatalf("%s: %v, want %s", name, err, code)
		}
	}
	setQuery := func(k, v string) func(*http.Request) {
		return func(r *http.Request) {
			q := r.URL.Query()
			q.Set(k, v)
			r.URL.RawQuery = q.Encode()
		}
	}
	expectCode("no cookie", "oidc_failed", func(r *http.Request) { r.Header.Del("Cookie") })
	expectCode("garbage cookie", "oidc_failed", func(r *http.Request) {
		r.Header.Set("Cookie", CookieName+"=garbage")
	})
	expectCode("foreign state", "oidc_failed", setQuery("state", "other"))
	expectCode("no code", "oidc_failed", setQuery("code", ""))
	expectCode("wrong code", "oidc_failed", setQuery("code", "forged"))
	// Неверный PKCE-verifier: cookie с другим verifier при том же state.
	expectCode("wrong verifier", "oidc_failed", func(r *http.Request) {
		st, _ := readCookie(r)
		st.Verifier = "wrong-verifier-wrong-verifier-wrong-verifier-123"
		r.Header.Set("Cookie", encodedCookie(t, st))
	})
	expectCode("provider error", "oidc_denied", setQuery("error", "access_denied"))

	idp.Mutate(func(c map[string]any) { c["nonce"] = "other" })
	expectCode("foreign nonce", "oidc_failed", nil)
	idp.Mutate(func(c map[string]any) { delete(c, "nonce") })
	expectCode("no nonce", "oidc_failed", nil)
	idp.Mutate(func(c map[string]any) { c["aud"] = "other-client" })
	expectCode("foreign aud", "oidc_failed", nil)
	idp.Mutate(func(c map[string]any) { c["iss"] = "https://evil.example" })
	expectCode("foreign iss", "oidc_failed", nil)
	idp.Mutate(func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() })
	expectCode("expired", "oidc_failed", nil)
	idp.Mutate(nil)
	idp.WrongKey(true)
	expectCode("bad signature", "oidc_failed", nil)
	idp.WrongKey(false)

	idp.Deny(true)
	_, flow, err := login(t, p, nil)
	if Code(err) != "oidc_denied" || flow.Redirect != "/books" {
		t.Fatalf("denied: %v %+v", err, flow)
	}
	// Без cookie режим неизвестен: вход с возвратом на «/».
	_, flow, err = login(t, p, func(r *http.Request) { r.Header.Del("Cookie") })
	if Code(err) != "oidc_denied" || flow.Redirect != "/" || flow.Link {
		t.Fatalf("denied without cookie: %v %+v", err, flow)
	}
}

func encodedCookie(t *testing.T, st flowState) string {
	t.Helper()
	data, _ := json.Marshal(st)
	r := httptest.NewRecorder()
	http.SetCookie(r, &http.Cookie{Name: CookieName, Value: base64.RawURLEncoding.EncodeToString(data)})
	return strings.SplitN(r.Header().Get("Set-Cookie"), ";", 2)[0]
}
