package apiv1

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/buildinfo"
	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
)

// maxBodyBytes ограничивает тело запроса: формы панели — сотни байт.
const maxBodyBytes = 64 << 10

// Server реализует ServerInterface.
type Server struct {
	Accounts *accounts.Service
	Auth     *panelauth.Auth
	Setup    *panelauth.Setup
	Progress *progress.Store
	Log      *slog.Logger
}

var _ ServerInterface = (*Server)(nil)

func (*Server) GetHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Health{Status: Ok})
}

func (*Server) GetVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Version{Version: buildinfo.Version, Commit: buildinfo.CommitOrVCS()})
}

func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if !decode(w, r, &req) {
		return
	}
	u, err := s.Accounts.VerifyPanelPassword(r.Context(), req.Username, req.Password, s.Auth.ClientAddr(r))
	if s.authFailed(w, r, req.Username, err, http.StatusUnauthorized) {
		return
	}
	s.openSession(w, r, u)
}

func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	cur, err := s.Auth.Session(r)
	switch {
	case err == nil:
		if err := s.Accounts.DeleteSession(r.Context(), cur.Session.ID); err != nil {
			s.internal(w, "logout", err)
			return
		}
	case !errors.Is(err, accounts.ErrUnauthorized):
		s.internal(w, "logout", err)
		return
	}
	panelauth.ClearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (*Server) GetMe(w http.ResponseWriter, r *http.Request) {
	cur, _ := panelauth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, me(cur.User))
}

func (s *Server) ChangePassword(w http.ResponseWriter, r *http.Request) {
	cur, _ := panelauth.FromContext(r.Context())
	var req ChangePasswordRequest
	if !decode(w, r, &req) {
		return
	}
	// Длина проверяется до текущего пароля: ошибка ввода не тратит бюджет.
	if !accounts.ValidPanelPassword(req.NewPassword) {
		writeError(w, http.StatusBadRequest, "invalid_password", accounts.ErrInvalidPassword.Error())
		return
	}
	_, err := s.Accounts.VerifyPanelPassword(r.Context(), cur.User.Username, req.CurrentPassword, s.Auth.ClientAddr(r))
	if s.authFailed(w, r, cur.User.Username, err, http.StatusForbidden) {
		return
	}
	if err := s.Accounts.SetPanelPassword(r.Context(), cur.User.Username, req.NewPassword, cur.Session.ID); err != nil {
		s.internal(w, "change password", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetSetup(w http.ResponseWriter, r *http.Request) {
	required, err := s.Setup.Required(r.Context())
	if err != nil {
		s.internal(w, "setup status", err)
		return
	}
	writeJSON(w, http.StatusOK, SetupStatus{Required: required})
}

func (s *Server) CompleteSetup(w http.ResponseWriter, r *http.Request) {
	var req SetupRequest
	if !decode(w, r, &req) {
		return
	}
	u, err := s.Setup.Complete(r.Context(), req.Code, req.Username, req.Password, s.Auth.ClientAddr(r))
	var rl *accounts.RateLimitedError
	switch {
	case err == nil:
		s.Log.Info("panel: setup completed", "user", u.Username)
		s.openSession(w, r, u)
	case errors.As(err, &rl):
		panelauth.WriteRateLimited(w, rl.RetryAfter)
	case errors.Is(err, panelauth.ErrInvalidSetupCode):
		s.Auth.LogFailure(r, req.Username, err.Error())
		writeError(w, http.StatusForbidden, "invalid_setup_code", err.Error())
	case errors.Is(err, accounts.ErrSetupCompleted):
		writeError(w, http.StatusConflict, "setup_completed", err.Error())
	case errors.Is(err, accounts.ErrInvalidUsername):
		writeError(w, http.StatusBadRequest, "invalid_username", err.Error())
	case errors.Is(err, accounts.ErrInvalidPassword):
		writeError(w, http.StatusBadRequest, "invalid_password", err.Error())
	default:
		s.internal(w, "setup", err)
	}
}

// authFailed отвечает на ошибку проверки пароля панели: блокировка — 429,
// неверный пароль — status с invalid_credentials и строкой в лог.
func (s *Server) authFailed(w http.ResponseWriter, r *http.Request, user string, err error, status int) bool {
	var rl *accounts.RateLimitedError
	switch {
	case err == nil:
		return false
	case errors.As(err, &rl):
		panelauth.WriteRateLimited(w, rl.RetryAfter)
	case errors.Is(err, accounts.ErrUnauthorized):
		s.Auth.LogFailure(r, user, err.Error())
		writeError(w, status, "invalid_credentials", "invalid username or password")
	default:
		s.internal(w, "verify panel password", err)
	}
	return true
}

func (s *Server) openSession(w http.ResponseWriter, r *http.Request, u accounts.User) {
	token, _, err := s.Accounts.CreateSession(r.Context(), u.ID)
	if err != nil {
		s.internal(w, "create session", err)
		return
	}
	panelauth.SetCookie(w, r, token)
	writeJSON(w, http.StatusOK, me(u))
}

func (s *Server) internal(w http.ResponseWriter, op string, err error) {
	s.Log.Error("panel: "+op, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal server error")
}

func me(u accounts.User) Me {
	return Me{Username: u.Username, Role: Role(u.Role)}
}

// decode читает JSON-тело в v; при ошибке отвечает 400 bad_request.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

// NewHandler возвращает обработчик всего префикса /api/. Неизвестный путь
// получает JSON 404, а не index.html. Сессию проверяет middleware операций,
// CSRF — защита от чужого origin на весь префикс.
func NewHandler(s *Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown API endpoint")
	})
	return panelauth.CrossOrigin(HandlerWithOptions(s, StdHTTPServerOptions{
		BaseRouter:  mux,
		Middlewares: []MiddlewareFunc{s.Auth.Middleware},
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		},
	}))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, Error{Code: code, Message: message})
}
