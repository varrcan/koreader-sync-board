package apiv1

import (
	"errors"
	"net/http"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
)

func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	users, err := s.Accounts.Users(r.Context())
	if err != nil {
		s.internal(w, "list users", err)
		return
	}
	out := make([]UserSummary, 0, len(users))
	for _, u := range users {
		out = append(out, UserSummary{
			Id:           u.ID,
			Username:     u.Username,
			Role:         Role(u.Role),
			Disabled:     u.DisabledAt != nil,
			CreatedAt:    u.CreatedAt.UTC(),
			HasPassword:  u.HasPassword,
			OidcLinked:   u.OIDCLinked,
			DeviceCount:  u.DeviceCount,
			LastActiveAt: utc(u.LastActiveAt),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CreateUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	var req CreateUserRequest
	if !decode(w, r, &req) {
		return
	}
	u, pass, err := s.Accounts.CreateUserWithPassword(r.Context(), cur.User.ID, req.Username, accounts.Role(req.Role))
	if s.userFailed(w, "create user", err) {
		return
	}
	s.Log.Info("panel: user created", "user", cur.User.Username, "target", u.Username, "role", u.Role)
	writeJSON(w, http.StatusCreated, IssuedUserPassword{Username: u.Username, Password: pass})
}

func (s *Server) UpdateUser(w http.ResponseWriter, r *http.Request, id UserID) {
	if !requireAdmin(w, r) {
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	var req UpdateUserRequest
	if !decode(w, r, &req) {
		return
	}
	upd := accounts.UserUpdate{Disabled: req.Disabled}
	if req.Role != nil {
		role := accounts.Role(*req.Role)
		upd.Role = &role
	}
	target, err := s.Accounts.UpdateUser(r.Context(), cur.User.ID, id, upd)
	if s.userFailed(w, "update user", err) {
		return
	}
	s.Log.Info("panel: user updated", "user", cur.User.Username, "target", target, "role", req.Role, "disabled", req.Disabled)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DeleteUser(w http.ResponseWriter, r *http.Request, id UserID) {
	if !requireAdmin(w, r) {
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	target, err := s.Accounts.AdminDeleteUser(r.Context(), cur.User.ID, id)
	if s.userFailed(w, "delete user", err) {
		return
	}
	s.Log.Info("panel: user deleted", "user", cur.User.Username, "target", target)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ResetUserPassword(w http.ResponseWriter, r *http.Request, id UserID) {
	if !requireAdmin(w, r) {
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	target, err := s.Accounts.UserByID(r.Context(), id)
	if s.userFailed(w, "reset user password", err) {
		return
	}
	// Свою строку отсекает сервис: self_action важнее настройки входа.
	if id != cur.User.ID && s.passwordDisabled(target) {
		writeError(w, http.StatusForbidden, "password_login_disabled", "password login is disabled for users linked to OIDC")
		return
	}
	username, pass, err := s.Accounts.ResetPanelPassword(r.Context(), cur.User.ID, id)
	if s.userFailed(w, "reset user password", err) {
		return
	}
	s.Log.Info("panel: user password reset", "user", cur.User.Username, "target", username)
	writeJSON(w, http.StatusOK, IssuedUserPassword{Username: username, Password: pass})
}

// UnlinkUserOIDC работает и без настроенного провайдера: привязка могла остаться
// с того времени, когда он был.
func (s *Server) UnlinkUserOIDC(w http.ResponseWriter, r *http.Request, id UserID) {
	if !requireAdmin(w, r) {
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	target, err := s.Accounts.AdminUnlinkIdentity(r.Context(), cur.User.ID, id)
	if s.userFailed(w, "unlink user oidc", err) {
		return
	}
	s.Log.Info("panel: user oidc unlinked", "user", cur.User.Username, "target", target)
	w.WriteHeader(http.StatusNoContent)
}

// userFailed отвечает на ошибку операции над пользователем.
func (s *Server) userFailed(w http.ResponseWriter, op string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, accounts.ErrSelf):
		writeError(w, http.StatusConflict, "self_action", err.Error())
	case errors.Is(err, accounts.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, accounts.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user_not_found", err.Error())
	case errors.Is(err, accounts.ErrUserExists):
		writeError(w, http.StatusConflict, "user_exists", err.Error())
	case errors.Is(err, accounts.ErrInvalidUsername):
		writeError(w, http.StatusBadRequest, "invalid_username", err.Error())
	case errors.Is(err, accounts.ErrInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		s.internal(w, op, err)
	}
	return true
}
