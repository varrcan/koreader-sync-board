package apiv1

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/config"
	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
	"github.com/varrcan/koreader-sync-board/server/internal/sso"
	"github.com/varrcan/koreader-sync-board/server/internal/sso/ssotest"
)

// oidcEnv — сервер с OIDC; KSB_PUBLIC_URL — адрес тестового сервера.
func oidcEnv(t *testing.T, mod func(*config.OIDC)) (*env, *ssotest.IdP) {
	t.Helper()
	e := newEnv(t)
	idp := ssotest.New(t, "ksb")
	cfg := &config.OIDC{
		Issuer: idp.URL, ClientID: "ksb", Name: "Zitadel",
		Scopes: []string{"openid", "profile"}, UsernameClaim: "preferred_username",
		PublicURL: e.url,
	}
	if mod != nil {
		mod(cfg)
	}
	e.srv.SSO = sso.New(cfg)
	return e, idp
}

func autoCreate(c *config.OIDC) { c.AutoCreate = true }

func adminClaim(c *config.OIDC) { c.AdminClaim, c.AdminValue = "groups", "ksb-admins" }

// hop — один ответ в цепочке перенаправлений.
type hop struct {
	url    string
	status int
	header http.Header
}

// oidc проходит цепочку перенаправлений браузера от target (путь API или
// абсолютный адрес), пока она идёт через API и провайдера, и возвращает адрес
// страницы панели, на которую она привела.
func (b *browser) oidc(target string) (string, []hop) {
	b.e.t.Helper()
	c := &http.Client{Jar: b.c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	base, _ := url.Parse(b.e.url)
	next, err := base.Parse(target)
	if err != nil {
		b.e.t.Fatal(err)
	}
	var hops []hop
	for range 10 {
		req, _ := http.NewRequest("GET", next.String(), nil)
		for k, v := range b.header {
			req.Header.Set(k, v)
		}
		res, err := c.Do(req)
		if err != nil {
			b.e.t.Fatal(err)
		}
		res.Body.Close()
		hops = append(hops, hop{url: next.String(), status: res.StatusCode, header: res.Header})
		if res.StatusCode != http.StatusFound {
			b.e.t.Fatalf("%s: status %d", next, res.StatusCode)
		}
		loc, err := next.Parse(res.Header.Get("Location"))
		if err != nil {
			b.e.t.Fatal(err)
		}
		if loc.Host == base.Host && !strings.HasPrefix(loc.Path, "/api/") {
			return loc.RequestURI(), hops
		}
		next = loc
	}
	b.e.t.Fatal("redirect loop")
	return "", nil
}

func (b *browser) oidcLogin(redirect string) string {
	b.e.t.Helper()
	page, _ := b.oidc("/api/v1/auth/oidc/login?redirect=" + url.QueryEscape(redirect))
	return page
}

// link — привязка из сессии: POST /me/oidc и переход по authorize_url.
func (b *browser) link() string {
	b.e.t.Helper()
	r := b.do("POST", "/api/v1/me/oidc", nil)
	r.expect(b.e.t, "start link", 200, "")
	au, _ := r.body["authorize_url"].(string)
	page, _ := b.oidc(au)
	return page
}

func expectPage(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: %q, want %q", name, got, want)
	}
}

func (e *env) user(name string) accounts.User {
	e.t.Helper()
	users, err := e.acc.ListUsers(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, u := range users {
		if u.Username == name {
			return u
		}
	}
	e.t.Fatalf("no user %q", name)
	return accounts.User{}
}

func (e *env) link(name, sub string) {
	e.t.Helper()
	if err := e.acc.LinkIdentity(e.ctx, e.user(name).ID, e.srv.SSO.Issuer(), sub); err != nil {
		e.t.Fatal(err)
	}
}

func TestAuthMethods(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	r := b.do("GET", "/api/v1/auth/methods", nil)
	r.expect(t, "methods", 200, "")
	if r.body["password"] != "on" || r.body["oidc"] != nil {
		t.Fatalf("without OIDC: %v", r.body)
	}
	e, _ = oidcEnv(t, nil)
	r = e.browser().do("GET", "/api/v1/auth/methods", nil)
	if o, _ := r.body["oidc"].(map[string]any); r.body["password"] != "on" || o["name"] != "Zitadel" || o["origin"] != e.url {
		t.Fatalf("with OIDC: %v", r.body)
	}
	e.srv.PasswordLoginUnlinkedOnly = true
	if r := e.browser().do("GET", "/api/v1/auth/methods", nil); r.body["password"] != "unlinked" {
		t.Fatalf("password off: %v", r.body)
	}
}

func TestOIDCNotConfigured(t *testing.T) {
	e := newEnv(t)
	e.admin()
	b := e.browser()
	b.do("GET", "/api/v1/auth/oidc/login", nil).expect(t, "login", 404, "not_found")
	b.do("GET", "/api/v1/auth/oidc/callback", nil).expect(t, "callback", 404, "not_found")
	b.do("POST", "/api/v1/me/oidc", nil).expect(t, "link without session", 401, "unauthorized")
	b.login("admin", "admin-password").expect(t, "login", 200, "")
	b.do("POST", "/api/v1/me/oidc", nil).expect(t, "link", 404, "not_found")
	b.do("DELETE", "/api/v1/me/oidc", nil).expect(t, "unlink", 404, "not_found")
	r := b.do("GET", "/api/v1/me", nil)
	if r.body["has_password"] != true || r.body["oidc_linked"] != false {
		t.Fatalf("me %v", r.body)
	}
}

func TestOIDCLoginLinkedUser(t *testing.T) {
	e, idp := oidcEnv(t, nil)
	e.admin()
	e.link("admin", "123")
	idp.SetClaims(map[string]any{"sub": "123", "preferred_username": "someone-else"})
	b := e.browser()
	page, hops := b.oidc("/api/v1/auth/oidc/login?redirect=" + url.QueryEscape("/statistics?tab=week"))
	expectPage(t, "login", page, "/statistics?tab=week")
	// Первый ответ ставит cookie потока, callback её стирает и ставит сессию.
	if c := hops[0].header.Get("Set-Cookie"); !strings.Contains(c, "ksb_oidc=") || !strings.Contains(c, "Path=/api/v1/auth/oidc/callback") {
		t.Fatalf("login Set-Cookie %q", c)
	}
	cb := hops[len(hops)-1].header.Values("Set-Cookie")
	if !strings.Contains(strings.Join(cb, "\n"), "ksb_oidc=; Path=/api/v1/auth/oidc/callback; Max-Age=0") ||
		!strings.Contains(strings.Join(cb, "\n"), "ksb_session=") {
		t.Fatalf("callback Set-Cookie %q", cb)
	}
	r := b.do("GET", "/api/v1/me", nil)
	if r.body["username"] != "admin" || r.body["oidc_linked"] != true || r.body["has_password"] != true {
		t.Fatalf("me %v", r.body)
	}
	// Внешний redirect заменяется на «/».
	expectPage(t, "evil redirect", e.browser().oidcLogin("//evil.example"), "/")
}

func TestOIDCLoginWrongHost(t *testing.T) {
	e, _ := oidcEnv(t, nil)
	req, _ := http.NewRequest("GET", e.url+"/api/v1/auth/oidc/login?redirect=/books", nil)
	req.Host = "192.168.1.5:8080"
	res, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 302 || res.Header.Get("Location") != e.url+"/api/v1/auth/oidc/login?redirect=/books" ||
		res.Header.Get("Set-Cookie") != "" {
		t.Fatalf("%d %q %q", res.StatusCode, res.Header.Get("Location"), res.Header.Get("Set-Cookie"))
	}
}

var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func TestOIDCCallbackErrors(t *testing.T) {
	e, idp := oidcEnv(t, nil)
	b := e.browser()
	// До первого пользователя — setup_required, даже с автосозданием.
	e.srv.SSO = sso.New(&config.OIDC{Issuer: idp.URL, ClientID: "ksb", UsernameClaim: "preferred_username",
		PublicURL: e.url, AutoCreate: true})
	idp.SetClaims(map[string]any{"sub": "1", "preferred_username": "bob"})
	expectPage(t, "empty db", b.oidcLogin("/"), "/login?oidc_error=setup_required")
	if n, _ := e.acc.CountUsers(e.ctx); n != 0 {
		t.Fatalf("%d users", n)
	}

	e, idp = oidcEnv(t, nil)
	e.admin()
	b = e.browser()
	// Совпадение имени ничего не связывает.
	idp.SetClaims(map[string]any{"sub": "1", "preferred_username": "admin"})
	expectPage(t, "not linked", b.oidcLogin("/"), "/login?oidc_error=oidc_not_linked")
	if e.user("admin").OIDCLinked {
		t.Fatal("linked by name")
	}

	idp.Deny(true)
	expectPage(t, "denied", b.oidcLogin("/books"), "/login?oidc_error=oidc_denied")
	idp.Deny(false)

	// Возврат только с error, без code и без cookie: 302, а не 400.
	page, hops := b.oidc("/api/v1/auth/oidc/callback?error=access_denied&state=x")
	expectPage(t, "error only", page, "/login?oidc_error=oidc_denied")
	if c := hops[0].header.Get("Set-Cookie"); !strings.Contains(c, "ksb_oidc=;") {
		t.Fatalf("cookie not cleared: %q", c)
	}
	page, _ = b.oidc("/api/v1/auth/oidc/callback")
	expectPage(t, "no params", page, "/login?oidc_error=oidc_failed")

	e.link("admin", "1")
	if err := e.acc.DisableUser(e.ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	expectPage(t, "disabled", b.oidcLogin("/"), "/login?oidc_error=account_disabled")

	idp.Down(true)
	e.srv.SSO = sso.New(&config.OIDC{Issuer: idp.URL, ClientID: "ksb", PublicURL: e.url})
	expectPage(t, "unavailable", b.oidcLogin("/"), "/login?oidc_error=oidc_unavailable")
	b.do("GET", "/api/v1/me", nil).expect(t, "no session", 401, "unauthorized")
}

// Неудачи OIDC не трогают ограничитель и не пишут строку для fail2ban.
func TestOIDCFailuresNotRateLimited(t *testing.T) {
	e, _ := oidcEnv(t, nil)
	e.admin()
	b := e.browser()
	for range ratelimit.MaxFails + 1 {
		page, _ := b.oidc("/api/v1/auth/oidc/callback?code=x&state=forged")
		expectPage(t, "forged", page, "/login?oidc_error=oidc_failed")
	}
	b.login("admin", "admin-password").expect(t, "password login", 200, "")
	log := e.logs.String()
	if strings.Contains(log, "panel: authorization failed") {
		t.Fatalf("failure line for OIDC:\n%s", log)
	}
	if !strings.Contains(log, `"msg":"panel: oidc login failed","reason":"oidc_failed: no flow cookie","ip":"127.0.0.1"`) {
		t.Fatalf("no oidc failure line:\n%s", log)
	}
	if strings.Contains(log, "code=x") {
		t.Fatal("authorization code logged")
	}
}

func TestOIDCAutoCreate(t *testing.T) {
	e, idp := oidcEnv(t, autoCreate)
	e.admin()
	idp.SetClaims(map[string]any{"sub": "b1", "preferred_username": "bob"})
	b := e.browser()
	expectPage(t, "create", b.oidcLogin("/books"), "/books")
	r := b.do("GET", "/api/v1/me", nil)
	if r.body["username"] != "bob" || r.body["role"] != "user" || r.body["has_password"] != false || r.body["oidc_linked"] != true {
		t.Fatalf("me %v", r.body)
	}
	expectPage(t, "again", e.browser().oidcLogin("/"), "/")
	if n, _ := e.acc.CountUsers(e.ctx); n != 2 {
		t.Fatalf("%d users", n)
	}

	idp.SetClaims(map[string]any{"sub": "a1", "preferred_username": "admin"})
	expectPage(t, "taken", e.browser().oidcLogin("/"), "/login?oidc_error=oidc_username_taken")
	if e.user("admin").OIDCLinked {
		t.Fatal("existing user linked")
	}
	for _, claims := range []map[string]any{{"sub": "c1"}, {"sub": "c1", "preferred_username": "a:b"}, {"sub": "c1", "preferred_username": 7}} {
		idp.SetClaims(claims)
		expectPage(t, fmt.Sprint(claims), e.browser().oidcLogin("/"), "/login?oidc_error=oidc_invalid_username")
	}
}

func TestOIDCAutoCreateRace(t *testing.T) {
	e, idp := oidcEnv(t, autoCreate)
	e.admin()
	idp.SetClaims(map[string]any{"sub": "b1", "preferred_username": "bob"})
	const n = 4
	pages := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		b := e.browser()
		wg.Go(func() { pages[i] = b.oidcLogin("/") })
	}
	wg.Wait()
	for i, p := range pages {
		expectPage(t, fmt.Sprint("login ", i), p, "/")
	}
	if c, _ := e.acc.CountUsers(e.ctx); c != 2 {
		t.Fatalf("%d users", c)
	}
}

func TestOIDCRoleClaim(t *testing.T) {
	e, idp := oidcEnv(t, func(c *config.OIDC) { autoCreate(c); adminClaim(c) })
	e.admin()
	idp.SetClaims(map[string]any{"sub": "b1", "preferred_username": "bob", "groups": []any{"ksb-admins"}})
	expectPage(t, "create admin", e.browser().oidcLogin("/"), "/")
	if e.user("bob").Role != accounts.RoleAdmin {
		t.Fatal("not promoted on create")
	}
	idp.SetClaims(map[string]any{"sub": "b1", "preferred_username": "bob", "groups": []any{"users"}})
	expectPage(t, "demote", e.browser().oidcLogin("/"), "/")
	if e.user("bob").Role != accounts.RoleUser {
		t.Fatal("not demoted")
	}
	if !strings.Contains(e.logs.String(), `"msg":"panel: oidc role changed","user":"bob","from":"admin","to":"user"`) {
		t.Fatalf("role change not logged:\n%s", e.logs)
	}
	idp.SetClaims(map[string]any{"sub": "b1", "groups": []any{"ksb-admins"}})
	expectPage(t, "promote", e.browser().oidcLogin("/"), "/")
	if e.user("bob").Role != accounts.RoleAdmin {
		t.Fatal("not promoted")
	}

	// Без настройки роль не меняется.
	e, idp = oidcEnv(t, nil)
	e.admin()
	e.link("admin", "a1")
	idp.SetClaims(map[string]any{"sub": "a1"})
	expectPage(t, "no role config", e.browser().oidcLogin("/"), "/")
	if e.user("admin").Role != accounts.RoleAdmin {
		t.Fatal("role changed without config")
	}
}

func TestOIDCLinkAndUnlink(t *testing.T) {
	e, idp := oidcEnv(t, adminClaim)
	e.admin()
	b := e.browser()
	b.login("admin", "admin-password").expect(t, "login", 200, "")
	idp.SetClaims(map[string]any{"sub": "a1", "groups": []any{"users"}})
	expectPage(t, "link", b.link(), "/account?oidc=linked")
	r := b.do("GET", "/api/v1/me", nil)
	if r.body["oidc_linked"] != true {
		t.Fatalf("me %v", r.body)
	}
	// Роль по claim применяется и при привязке.
	if e.user("admin").Role != accounts.RoleUser {
		t.Fatal("role not applied on link")
	}
	if err := e.acc.SetRole(e.ctx, "admin", accounts.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	idp.SetClaims(map[string]any{"sub": "a1", "groups": []any{"ksb-admins"}})
	b.do("POST", "/api/v1/auth/logout", nil).expect(t, "logout", 204, "")
	expectPage(t, "login after link", b.oidcLogin("/"), "/")
	if r := b.do("GET", "/api/v1/me", nil); r.body["username"] != "admin" {
		t.Fatalf("me %v", r.body)
	}

	// Учётка провайдера уже у другого пользователя.
	if _, err := e.acc.CreateUser(e.ctx, "bob", accounts.RoleUser); err != nil {
		t.Fatal(err)
	}
	if err := e.acc.SetPanelPassword(e.ctx, "bob", "bob-password", 0); err != nil {
		t.Fatal(err)
	}
	bob := e.browser()
	bob.login("bob", "bob-password").expect(t, "bob login", 200, "")
	expectPage(t, "taken", bob.link(), "/account?oidc_error=oidc_already_linked")
	if e.user("bob").OIDCLinked {
		t.Fatal("bob linked")
	}
	// Ошибки потока в режиме привязки ведут на «Учётную запись».
	idp.Deny(true)
	expectPage(t, "denied link", bob.link(), "/account?oidc_error=oidc_denied")
	idp.Deny(false)

	// Сессию удалили, пока пользователь был у провайдера.
	idp.SetClaims(map[string]any{"sub": "b1"})
	r = bob.do("POST", "/api/v1/me/oidc", nil)
	if err := e.acc.SetPanelPassword(e.ctx, "bob", "bob-password-2", 0); err != nil {
		t.Fatal(err)
	}
	au, _ := r.body["authorize_url"].(string)
	page, _ := bob.oidc(au)
	expectPage(t, "session expired", page, "/account?oidc_error=oidc_session_expired")
	if e.user("bob").OIDCLinked {
		t.Fatal("linked without session")
	}

	// Отвязка разрешена при выключенном входе по паролю, если пароль есть.
	e.srv.PasswordLoginUnlinkedOnly = true
	b.do("DELETE", "/api/v1/me/oidc", nil).expect(t, "unlink", 204, "")
	expectPage(t, "after unlink", e.browser().oidcLogin("/"), "/login?oidc_error=oidc_not_linked")
	e.browser().login("admin", "admin-password").expect(t, "password after unlink", 200, "")
}

func TestOIDCLinkGuards(t *testing.T) {
	e, idp := oidcEnv(t, autoCreate)
	e.admin()
	idp.SetClaims(map[string]any{"sub": "b1", "preferred_username": "bob"})
	b := e.browser()
	b.oidcLogin("/")
	b.do("DELETE", "/api/v1/me/oidc", nil).expect(t, "last method", 409, "last_login_method")
	if !e.user("bob").OIDCLinked {
		t.Fatal("unlinked")
	}

	evil := e.browser()
	evil.c.Jar = b.c.Jar
	evil.header = map[string]string{"Sec-Fetch-Site": "cross-site"}
	evil.do("POST", "/api/v1/me/oidc", nil).expect(t, "cross-site", 403, "cross_origin")

	req, _ := http.NewRequest("POST", e.url+"/api/v1/me/oidc", nil)
	req.Host = "192.168.1.5:8080"
	u, _ := url.Parse(e.url)
	for _, c := range b.c.Jar.Cookies(u) {
		req.AddCookie(c)
	}
	res, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 409 || res.Header.Get("Set-Cookie") != "" {
		t.Fatalf("wrong host: %d %q", res.StatusCode, res.Header.Get("Set-Cookie"))
	}
}

func TestPasswordLoginDisabled(t *testing.T) {
	e, idp := oidcEnv(t, nil)
	e.srv.PasswordLoginUnlinkedOnly = true
	// Администратор из ADMIN_* (без сессии) входит паролем и привязывает OIDC.
	if _, err := e.acc.Bootstrap(e.ctx, "admin", "admin-password"); err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	b.login("admin", "admin-password").expect(t, "unlinked admin", 200, "")
	idp.SetClaims(map[string]any{"sub": "a1"})
	expectPage(t, "link", b.link(), "/account?oidc=linked")

	before := len(e.logs.String())
	r := e.browser().login("admin", "admin-password")
	r.expect(t, "linked admin", 403, "password_login_disabled")
	if strings.Contains(r.header.Get("Set-Cookie"), "ksb_session") {
		t.Fatal("session opened")
	}
	if strings.Contains(e.logs.String()[before:], "authorization failed") {
		t.Fatal("logged as failure")
	}
	e.browser().login("admin", "wrong-password").expect(t, "wrong password", 401, "invalid_credentials")
	expectPage(t, "oidc", e.browser().oidcLogin("/"), "/")

	// Смена пароля привязанного — 403 без argon2 и без расхода бюджета.
	for range ratelimit.MaxFails + 1 {
		b.do("PUT", "/api/v1/me/password", map[string]string{"current_password": "x", "new_password": "new-password"}).
			expect(t, "change", 403, "password_login_disabled")
	}
	if _, err := e.acc.CreateUser(e.ctx, "bob", accounts.RoleUser); err != nil {
		t.Fatal(err)
	}
	if err := e.acc.SetPanelPassword(e.ctx, "bob", "bob-password", 0); err != nil {
		t.Fatal(err)
	}
	e.browser().login("bob", "bob-password").expect(t, "unlinked bob after attempts", 200, "")
}

func TestPasswordLoginDisabledSetup(t *testing.T) {
	e, _ := oidcEnv(t, nil)
	e.srv.PasswordLoginUnlinkedOnly = true
	code := e.setup.Generate()
	b := e.browser()
	b.do("POST", "/api/v1/setup", map[string]string{"code": code, "username": "admin", "password": "admin-password"}).
		expect(t, "setup", 200, "")
	b.do("POST", "/api/v1/me/oidc", nil).expect(t, "link after setup", 200, "")
}

func TestFirstPassword(t *testing.T) {
	e, idp := oidcEnv(t, autoCreate)
	e.admin()
	idp.SetClaims(map[string]any{"sub": "b1", "preferred_username": "bob"})
	b, other := e.browser(), e.browser()
	b.oidcLogin("/")
	other.oidcLogin("/")
	b.do("PUT", "/api/v1/me/password", map[string]string{"new_password": "short"}).expect(t, "short", 400, "invalid_password")
	b.do("PUT", "/api/v1/me/password", map[string]string{"new_password": "bob-first-pass"}).expect(t, "first", 204, "")
	r := b.do("GET", "/api/v1/me", nil)
	if r.body["has_password"] != true {
		t.Fatalf("me %v", r.body)
	}
	other.do("GET", "/api/v1/me", nil).expect(t, "other session kept", 200, "")
	e.browser().login("bob", "bob-first-pass").expect(t, "password login", 200, "")
	if strings.Contains(e.logs.String(), "authorization failed") {
		t.Fatal("first password logged as failure")
	}
	b.do("DELETE", "/api/v1/me/oidc", nil).expect(t, "unlink allowed", 204, "")

	// С паролем отсутствие current_password — неверный пароль, бюджет расходуется.
	for range ratelimit.MaxFails {
		b.do("PUT", "/api/v1/me/password", map[string]string{"new_password": "another-pass"}).
			expect(t, "without current", 403, "invalid_credentials")
	}
	e.browser().login("bob", "bob-first-pass").expect(t, "budget spent", 429, "too_many_attempts")
}
