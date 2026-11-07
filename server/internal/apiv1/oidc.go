package apiv1

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
	"github.com/varrcan/koreader-sync-board/server/internal/sso"
)

func (s *Server) GetAuthMethods(w http.ResponseWriter, _ *http.Request) {
	m := AuthMethods{Password: PasswordOn}
	if s.PasswordLoginUnlinkedOnly {
		m.Password = PasswordUnlinked
	}
	if s.SSO != nil {
		m.Oidc = &OIDCMethod{Name: s.SSO.Name(), Origin: s.SSO.Origin()}
	}
	writeJSON(w, http.StatusOK, m)
}

// OidcLogin — навигация браузера, поэтому ошибки — 302 на экран входа, а не JSON.
func (s *Server) OidcLogin(w http.ResponseWriter, r *http.Request, params OidcLoginParams) {
	if s.oidcOff(w) {
		return
	}
	// Cookie потока ставится на хост запроса, а провайдер вернёт браузер на
	// публичный: весь поток должен идти там.
	if !s.SSO.OnPublicHost(r) {
		http.Redirect(w, r, s.SSO.PublicURL(r), http.StatusFound)
		return
	}
	var redirect string
	if params.Redirect != nil {
		redirect = *params.Redirect
	}
	authURL, cookie, err := s.SSO.Start(r.Context(), redirect, false, panelauth.IsHTTPS(r))
	if err != nil {
		s.oidcFailed(w, r, sso.Flow{}, sso.Code(err), err)
		return
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// OidcCallback — общий возврат для входа и привязки: redirect_uri у провайдера
// регистрируется один. Параметры разбирает sso.Finish.
func (s *Server) OidcCallback(w http.ResponseWriter, r *http.Request, _ OidcCallbackParams) {
	if s.oidcOff(w) {
		return
	}
	sso.ClearCookie(w, panelauth.IsHTTPS(r))
	id, flow, err := s.SSO.Finish(r)
	if err != nil {
		s.oidcFailed(w, r, flow, sso.Code(err), err)
		return
	}
	if flow.Link {
		s.linkCallback(w, r, flow, id)
		return
	}
	s.loginCallback(w, r, flow, id)
}

func (s *Server) loginCallback(w http.ResponseWriter, r *http.Request, flow sso.Flow, id sso.Identity) {
	ctx := r.Context()
	// До первого пользователя всё требует кода настройки, OIDC тоже.
	n, err := s.Accounts.CountUsers(ctx)
	if err != nil {
		s.oidcInternal(w, r, flow, "count users", err)
		return
	}
	if n == 0 {
		s.oidcFailed(w, r, flow, "setup_required", nil)
		return
	}
	u, err := s.Accounts.UserByIdentity(ctx, id.Issuer, id.Subject)
	if errors.Is(err, accounts.ErrNotFound) {
		var code string
		u, code, err = s.createOIDCUser(ctx, id)
		if code != "" {
			s.oidcFailed(w, r, flow, code, nil)
			return
		}
	}
	if err != nil {
		s.oidcInternal(w, r, flow, "find user by identity", err)
		return
	}
	if u.DisabledAt != nil {
		s.oidcFailed(w, r, flow, "account_disabled", nil)
		return
	}
	if !s.applyRole(w, r, flow, &u, id) {
		return
	}
	token, _, err := s.Accounts.CreateSession(ctx, u.ID)
	if err != nil {
		s.oidcInternal(w, r, flow, "create session", err)
		return
	}
	panelauth.SetCookie(w, r, token)
	http.Redirect(w, r, flow.Redirect, http.StatusFound)
}

// createOIDCUser создаёт пользователя для непривязанной учётки; code — код
// отказа. Конфликт в гонке двух первых входов одного sub разрешается повторным
// поиском: второй вход получает того же пользователя.
func (s *Server) createOIDCUser(ctx context.Context, id sso.Identity) (accounts.User, string, error) {
	if !s.SSO.AutoCreate() {
		return accounts.User{}, "oidc_not_linked", nil
	}
	if !accounts.ValidUsername(id.Username) {
		return accounts.User{}, "oidc_invalid_username", nil
	}
	role := accounts.RoleUser
	if id.Admin != nil && *id.Admin {
		role = accounts.RoleAdmin
	}
	u, err := s.Accounts.CreateOIDCUser(ctx, id.Username, role, id.Issuer, id.Subject)
	switch {
	case err == nil:
		s.Log.Info("panel: oidc user created", "user", u.Username, "role", u.Role)
		return u, "", nil
	case errors.Is(err, accounts.ErrIdentityTaken), errors.Is(err, accounts.ErrUserExists):
		u, err := s.Accounts.UserByIdentity(ctx, id.Issuer, id.Subject)
		if errors.Is(err, accounts.ErrNotFound) {
			return accounts.User{}, "oidc_username_taken", nil
		}
		return u, "", err
	default:
		return accounts.User{}, "", err
	}
}

func (s *Server) linkCallback(w http.ResponseWriter, r *http.Request, flow sso.Flow, id sso.Identity) {
	cur, err := s.Auth.Session(r)
	switch {
	case errors.Is(err, accounts.ErrUnauthorized):
		s.oidcFailed(w, r, flow, "oidc_session_expired", nil)
		return
	case err != nil:
		s.oidcInternal(w, r, flow, "session lookup", err)
		return
	}
	err = s.Accounts.LinkIdentity(r.Context(), cur.User.ID, id.Issuer, id.Subject)
	switch {
	case errors.Is(err, accounts.ErrIdentityTaken):
		s.oidcFailed(w, r, flow, "oidc_already_linked", nil)
		return
	case errors.Is(err, accounts.ErrNotFound):
		s.oidcFailed(w, r, flow, "oidc_session_expired", nil)
		return
	case err != nil:
		s.oidcInternal(w, r, flow, "link identity", err)
		return
	}
	s.Log.Info("panel: oidc linked", "user", cur.User.Username)
	u := cur.User
	if !s.applyRole(w, r, flow, &u, id) {
		return
	}
	http.Redirect(w, r, "/account?oidc=linked", http.StatusFound)
}

// applyRole выставляет роль по claim, если он настроен. false — ответ уже отправлен.
func (s *Server) applyRole(w http.ResponseWriter, r *http.Request, flow sso.Flow, u *accounts.User, id sso.Identity) bool {
	if id.Admin == nil {
		return true
	}
	want := accounts.RoleUser
	if *id.Admin {
		want = accounts.RoleAdmin
	}
	if u.Role == want {
		return true
	}
	if err := s.Accounts.SetRole(r.Context(), u.Username, want); err != nil {
		s.oidcInternal(w, r, flow, "set role", err)
		return false
	}
	s.Log.Info("panel: oidc role changed", "user", u.Username, "from", u.Role, "to", want)
	u.Role = want
	return true
}

func (s *Server) StartOIDCLink(w http.ResponseWriter, r *http.Request) {
	if s.oidcOff(w) {
		return
	}
	// Сессия есть только на текущем хосте, а провайдер вернёт браузер на
	// публичный: перенаправлением это не исправить.
	if !s.SSO.OnPublicHost(r) {
		writeError(w, http.StatusConflict, "oidc_wrong_host", "open the panel at "+s.SSO.Origin()+" to link OIDC")
		return
	}
	authURL, cookie, err := s.SSO.Start(r.Context(), "/account", true, panelauth.IsHTTPS(r))
	if err != nil {
		s.Log.Warn("panel: oidc login failed", "reason", err.Error(), "ip", s.Auth.ClientAddr(r).String())
		writeError(w, http.StatusServiceUnavailable, sso.Code(err), "OIDC provider is unavailable")
		return
	}
	http.SetCookie(w, cookie)
	writeJSON(w, http.StatusOK, OIDCLinkStart{AuthorizeUrl: authURL})
}

func (s *Server) UnlinkOIDC(w http.ResponseWriter, r *http.Request) {
	if s.oidcOff(w) {
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	// Без пароля панели отвязка оставила бы пользователя без способа входа.
	if !cur.User.HasPassword {
		writeError(w, http.StatusConflict, "last_login_method", "set a panel password before unlinking OIDC")
		return
	}
	if err := s.Accounts.UnlinkIdentity(r.Context(), cur.User.Username); err != nil {
		s.internal(w, "unlink oidc", err)
		return
	}
	s.Log.Info("panel: oidc unlinked", "user", cur.User.Username)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) oidcOff(w http.ResponseWriter) bool {
	if s.SSO == nil {
		writeError(w, http.StatusNotFound, "not_found", "OIDC is not configured")
		return true
	}
	return false
}

// oidcFailed пишет неудачу в лог и уводит браузер на страницу с кодом ошибки:
// при привязке — на «Учётную запись», иначе на вход. Строка не та, что у
// неудачного пароля (panel: authorization failed), и ограничитель не трогается:
// секрет здесь не подбирается, а fail2ban на отказ IdP забанил бы своего.
func (s *Server) oidcFailed(w http.ResponseWriter, r *http.Request, flow sso.Flow, code string, cause error) {
	reason := code
	if cause != nil {
		reason = cause.Error()
	}
	s.Log.Warn("panel: oidc login failed", "reason", reason, "ip", s.Auth.ClientAddr(r).String())
	page := "/login"
	if flow.Link {
		page = "/account"
	}
	http.Redirect(w, r, page+"?"+url.Values{"oidc_error": {code}}.Encode(), http.StatusFound)
}

func (s *Server) oidcInternal(w http.ResponseWriter, r *http.Request, flow sso.Flow, op string, err error) {
	s.Log.Error("panel: oidc "+op, "err", err)
	s.oidcFailed(w, r, flow, sso.ErrFailed.Error(), nil)
}
