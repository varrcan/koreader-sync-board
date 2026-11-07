// Package sso — вход в панель через OIDC-провайдера: Authorization Code + PKCE,
// state и nonce в cookie браузера, проверка ID-токена и разбор claims.
// Имя не oidc, чтобы не путать с импортом go-oidc.
//
// Discovery ленивый: провайдер запрашивается при первом входе, а неудача не
// кешируется. Так недоступный IdP не мешает старту сервера и kosync.
package sso

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/varrcan/koreader-sync-board/server/internal/config"
)

const (
	// CookieName — cookie с данными незавершённого потока.
	CookieName = "ksb_oidc"
	// CallbackPath — путь возврата с провайдера; cookie потока видна только ему.
	CallbackPath = "/api/v1/auth/oidc/callback"

	cookieTTL   = 10 * time.Minute
	httpTimeout = 10 * time.Second
)

// Ошибки потока; Code переводит их в коды API.
var (
	// ErrDenied — провайдер вернул error (пользователь отменил вход и т. п.).
	ErrDenied = errors.New("oidc_denied")
	// ErrFailed — нет cookie, не совпал state или nonce, обмен кода или проверка токена не прошли.
	ErrFailed = errors.New("oidc_failed")
	// ErrUnavailable — discovery провайдера не удался.
	ErrUnavailable = errors.New("oidc_unavailable")
)

// Code возвращает код API для ошибки потока: oidc_denied, oidc_failed или oidc_unavailable.
func Code(err error) string {
	for _, e := range []error{ErrDenied, ErrUnavailable} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return ErrFailed.Error()
}

// Identity — проверенная учётка провайдера.
type Identity struct {
	Issuer  string
	Subject string
	// Username — строковое значение claim имени; пусто, если его нет или он не строка.
	Username string
	// Admin — совпал ли claim роли; nil, если роль по claim не настроена.
	Admin *bool
}

// Flow — данные потока из cookie: куда вернуться и привязка это или вход. До
// разбора cookie (её нет) — вход с возвратом на «/».
type Flow struct {
	Redirect string
	Link     bool
}

// flowState — содержимое cookie ksb_oidc. Всё в ней — секреты самого браузера,
// подделать её чужой сайт не может, поэтому подпись не нужна.
type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Redirect string `json:"r"`
	Link     bool   `json:"l,omitempty"`
}

// Provider — ленивый OIDC-клиент.
type Provider struct {
	cfg        *config.OIDC
	publicHost string
	client     *http.Client

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

// New создаёт провайдера без обращения к IdP.
func New(cfg *config.OIDC) *Provider {
	u, _ := url.Parse(cfg.PublicURL) // проверен config
	return &Provider{cfg: cfg, publicHost: u.Host, client: &http.Client{Timeout: httpTimeout}}
}

// Name — подпись кнопки входа.
func (p *Provider) Name() string { return p.cfg.Name }

// Origin — публичный адрес панели (KSB_PUBLIC_URL).
func (p *Provider) Origin() string { return p.cfg.PublicURL }

// Issuer — issuer из конфигурации; после Verify он равен iss токена.
func (p *Provider) Issuer() string { return p.cfg.Issuer }

// AutoCreate — создавать пользователя при первом входе непривязанной учётки.
func (p *Provider) AutoCreate() bool { return p.cfg.AutoCreate }

// OnPublicHost — запрос пришёл на хост KSB_PUBLIC_URL. Иначе cookie потока
// осталась бы на другом хосте, а провайдер вернул бы браузер на публичный.
// Схему не сравниваем: перевод http→https на одном хосте делает прокси.
// X-Forwarded-Host учитывается, потому что прокси, не передающий Host (nginx
// без proxy_set_header Host), иначе зациклил бы перенаправление на публичный
// адрес. Подделав его, клиент сломает только собственный вход.
func (p *Provider) OnPublicHost(r *http.Request) bool {
	fwd, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	return strings.EqualFold(r.Host, p.publicHost) || strings.EqualFold(strings.TrimSpace(fwd), p.publicHost)
}

// PublicURL — тот же путь и query на публичном адресе.
func (p *Provider) PublicURL(r *http.Request) string {
	return p.cfg.PublicURL + r.URL.RequestURI()
}

// discover — OIDC-клиент после discovery. Ошибка не кешируется: следующий вход
// попробует снова. Из контекста NewProvider берёт только HTTP-клиента: ключи
// провайдер потом загружает на фоновом контексте, запрос, на котором прошёл
// discovery, их не ограничивает.
func (p *Provider) discover(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.oauth != nil {
		return p.oauth, p.verifier, nil
	}
	op, err := oidc.NewProvider(oidc.ClientContext(ctx, p.client), p.cfg.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	p.oauth = &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		Endpoint:     op.Endpoint(),
		RedirectURL:  p.cfg.RedirectURL(),
		Scopes:       p.cfg.Scopes,
	}
	p.verifier = op.Verifier(&oidc.Config{ClientID: p.cfg.ClientID})
	return p.oauth, p.verifier, nil
}

// Start начинает поток: возвращает адрес авторизации у провайдера и cookie
// потока. redirect, не являющийся путём внутри панели, заменяется на «/».
// secure — ставить ли cookie Secure (по тому же правилу, что cookie сессии).
func (p *Provider) Start(ctx context.Context, redirect string, link, secure bool) (string, *http.Cookie, error) {
	oc, _, err := p.discover(ctx)
	if err != nil {
		return "", nil, err
	}
	st := flowState{
		State:    rand.Text(),
		Nonce:    rand.Text(),
		Verifier: oauth2.GenerateVerifier(),
		Redirect: SafeRedirect(redirect),
		Link:     link,
	}
	data, _ := json.Marshal(st)
	cookie := &http.Cookie{
		Name:     CookieName,
		Value:    base64.RawURLEncoding.EncodeToString(data),
		Path:     CallbackPath,
		MaxAge:   int(cookieTTL / time.Second),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	authURL := oc.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
	return authURL, cookie, nil
}

// ClearCookie стирает cookie потока; callback делает это при любом исходе.
func ClearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Path:     CallbackPath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Finish завершает поток по возврату с провайдера. Flow заполнен, если cookie
// потока удалось прочитать, даже при ошибке: по нему выбирается страница для
// ошибки привязки. Cookie стирает вызывающий.
func (p *Provider) Finish(r *http.Request) (Identity, Flow, error) {
	flow := Flow{Redirect: "/"}
	st, cookieErr := readCookie(r)
	if cookieErr == nil {
		flow = Flow{Redirect: SafeRedirect(st.Redirect), Link: st.Link}
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return Identity{}, flow, fmt.Errorf("%w: %s", ErrDenied, e)
	}
	if cookieErr != nil {
		return Identity{}, flow, fmt.Errorf("%w: %w", ErrFailed, cookieErr)
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		return Identity{}, flow, fmt.Errorf("%w: state mismatch", ErrFailed)
	}
	code := q.Get("code")
	if code == "" {
		return Identity{}, flow, fmt.Errorf("%w: no code", ErrFailed)
	}
	oc, verifier, err := p.discover(r.Context())
	if err != nil {
		return Identity{}, flow, err
	}
	ctx := oidc.ClientContext(r.Context(), p.client)
	tok, err := oc.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return Identity{}, flow, fmt.Errorf("%w: exchange: %w", ErrFailed, err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, flow, fmt.Errorf("%w: no id_token in token response", ErrFailed)
	}
	idt, err := verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, flow, fmt.Errorf("%w: %w", ErrFailed, err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(st.Nonce)) != 1 {
		return Identity{}, flow, fmt.Errorf("%w: nonce mismatch", ErrFailed)
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return Identity{}, flow, fmt.Errorf("%w: %w", ErrFailed, err)
	}
	id := Identity{Issuer: p.cfg.Issuer, Subject: idt.Subject}
	id.Username, _ = claims[p.cfg.UsernameClaim].(string)
	if p.cfg.AdminClaim != "" {
		admin := claimMatches(claims[p.cfg.AdminClaim], p.cfg.AdminValue)
		id.Admin = &admin
	}
	return id, flow, nil
}

func readCookie(r *http.Request) (flowState, error) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return flowState{}, errors.New("no flow cookie")
	}
	data, err := base64.RawURLEncoding.DecodeString(c.Value)
	var st flowState
	if err == nil {
		err = json.Unmarshal(data, &st)
	}
	if err != nil || st.State == "" || st.Nonce == "" || st.Verifier == "" {
		return flowState{}, errors.New("malformed flow cookie")
	}
	return st, nil
}

// claimMatches — совпадает ли claim роли со значением: строка — равенство;
// массив — содержит такую строку или объект с таким ключом; объект — есть ключ
// (роли Zitadel: {"admin": {"<orgId>": "<domain>"}}).
func claimMatches(claim any, value string) bool {
	switch c := claim.(type) {
	case string:
		return c == value
	case []any:
		for _, item := range c {
			switch v := item.(type) {
			case string:
				if v == value {
					return true
				}
			case map[string]any:
				if _, ok := v[value]; ok {
					return true
				}
			}
		}
	case map[string]any:
		_, ok := c[value]
		return ok
	}
	return false
}

// SafeRedirect возвращает target, если это путь внутри панели, иначе «/».
// «//host» и «/\host» браузер понимает как адрес другого сайта, а табуляцию и
// переводы строк вырезает из URL, поэтому управляющие символы тоже отвергаются.
func SafeRedirect(target string) string {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, `/\`) ||
		strings.ContainsFunc(target, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "/"
	}
	return target
}
