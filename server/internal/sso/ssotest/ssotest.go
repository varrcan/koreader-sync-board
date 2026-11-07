// Package ssotest — тестовый OIDC-провайдер на httptest: discovery, JWKS,
// /authorize, сразу отвечающий 302 с кодом, и /token с проверкой PKCE.
package ssotest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// IdP — тестовый провайдер. Поля меняются тестом между входами.
type IdP struct {
	URL      string // issuer
	ClientID string

	mu sync.Mutex
	// claims — claims следующего ID-токена (sub, preferred_username, роли …).
	// iss, aud, exp, iat и nonce сервер добавляет сам; mutate может их подменить.
	claims map[string]any
	mutate func(map[string]any)
	// deny — /authorize возвращает error=access_denied.
	deny bool
	// down — discovery отвечает 503.
	down bool
	// wrongKey — подписывать ID-токен ключом, которого нет в JWKS.
	wrongKey bool

	key, other *rsa.PrivateKey
	codes      map[string]grant
	discovery  int
}

type grant struct {
	challenge, nonce, redirectURI string
}

// New запускает провайдера; клиент — clientID.
func New(t testing.TB, clientID string) *IdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &IdP{ClientID: clientID, key: key, other: other, codes: map[string]grant{}, claims: map[string]any{"sub": "1"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.wellKnown)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p.URL = srv.URL
	return p
}

// SetClaims задаёт claims следующих ID-токенов.
func (p *IdP) SetClaims(c map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims = maps.Clone(c)
}

// Mutate подменяет итоговые claims токена (nonce, aud, exp …); nil — без подмены.
func (p *IdP) Mutate(f func(map[string]any)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mutate = f
}

// Deny включает отказ на /authorize.
func (p *IdP) Deny(v bool) { p.set(&p.deny, v) }

// Down делает discovery недоступным.
func (p *IdP) Down(v bool) { p.set(&p.down, v) }

// WrongKey подписывает токены ключом не из JWKS.
func (p *IdP) WrongKey(v bool) { p.set(&p.wrongKey, v) }

// Discoveries — сколько раз запрашивали discovery успешно.
func (p *IdP) Discoveries() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discovery
}

func (p *IdP) set(f *bool, v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	*f = v
}

func (p *IdP) wellKnown(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	down := p.down
	if !down {
		p.discovery++
	}
	p.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *IdP) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &p.key.PublicKey, KeyID: "k1", Algorithm: string(jose.RS256), Use: "sig"},
	}})
}

func (p *IdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	bq := url.Values{"state": {q.Get("state")}}
	p.mu.Lock()
	if p.deny {
		bq.Set("error", "access_denied")
	} else {
		code := rand.Text()
		p.codes[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirectURI: back.String()}
		bq.Set("code", code)
	}
	p.mu.Unlock()
	back.RawQuery = bq.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (p *IdP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	clientID := r.PostForm.Get("client_id")
	if u, _, ok := r.BasicAuth(); ok {
		clientID, _ = url.QueryUnescape(u)
	}
	p.mu.Lock()
	g, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	claims := maps.Clone(p.claims)
	mutate, signKey := p.mutate, p.key
	if p.wrongKey {
		signKey = p.other
	}
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || clientID != p.ClientID || r.PostForm.Get("redirect_uri") != g.redirectURI ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	now := time.Now()
	claims["iss"] = p.URL
	claims["aud"] = p.ClientID
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(5 * time.Minute).Unix()
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	if mutate != nil {
		mutate(claims)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idToken, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": rand.Text(),
		"token_type":   "Bearer",
		"expires_in":   300,
		"id_token":     idToken,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
