package apiv1

import (
	"errors"
	"net/http"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
)

// scope — устройства, доступные владельцу сессии. Роль берётся из сессии,
// которую middleware сверяет с БД на каждом запросе, так что смена роли
// действует сразу.
func scope(cur panelauth.Current) accounts.Scope {
	return accounts.Scope{UserID: cur.User.ID, All: cur.User.Role == accounts.RoleAdmin}
}

func (s *Server) ListDevices(w http.ResponseWriter, r *http.Request) {
	cur, _ := panelauth.FromContext(r.Context())
	ds, err := s.Accounts.Devices(r.Context(), scope(cur))
	if err != nil {
		s.internal(w, "list devices", err)
		return
	}
	out := make([]Device, 0, len(ds))
	for _, d := range ds {
		out = append(out, device(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CreateDevice(w http.ResponseWriter, r *http.Request) {
	cur, _ := panelauth.FromContext(r.Context())
	var req CreateDeviceRequest
	if !decode(w, r, &req) {
		return
	}
	var (
		d    accounts.DeviceInfo
		pass string
		err  error
	)
	switch {
	case req.Username == nil || *req.Username == cur.User.Username:
		d, pass, err = s.Accounts.AddDeviceFor(r.Context(), cur.User.ID, req.Name)
	case cur.User.Role != accounts.RoleAdmin:
		writeError(w, http.StatusForbidden, "forbidden", "only an administrator can add devices for other users")
		return
	default:
		d, pass, err = s.Accounts.AddDevice(r.Context(), *req.Username, req.Name)
	}
	if s.deviceFailed(w, "create device", err) {
		return
	}
	s.Log.Info("panel: device added", "user", cur.User.Username, "owner", d.Owner, "device_id", d.ID)
	writeJSON(w, http.StatusCreated, DeviceWithPassword{Device: device(d), Password: pass})
}

func (s *Server) RenameDevice(w http.ResponseWriter, r *http.Request, id DeviceID) {
	cur, _ := panelauth.FromContext(r.Context())
	var req RenameDeviceRequest
	if !decode(w, r, &req) {
		return
	}
	d, err := s.Accounts.RenameDeviceByID(r.Context(), scope(cur), id, req.Name)
	if s.deviceFailed(w, "rename device", err) {
		return
	}
	writeJSON(w, http.StatusOK, device(d))
}

func (s *Server) ResetDevicePassword(w http.ResponseWriter, r *http.Request, id DeviceID) {
	cur, _ := panelauth.FromContext(r.Context())
	d, pass, err := s.Accounts.ResetDeviceKey(r.Context(), scope(cur), id)
	if s.deviceFailed(w, "reset device password", err) {
		return
	}
	s.Log.Info("panel: device password reset", "user", cur.User.Username, "owner", d.Owner, "device_id", d.ID)
	writeJSON(w, http.StatusOK, DeviceWithPassword{Device: device(d), Password: pass})
}

func (s *Server) RevokeDevice(w http.ResponseWriter, r *http.Request, id DeviceID) {
	cur, _ := panelauth.FromContext(r.Context())
	err := s.Accounts.RevokeDeviceByID(r.Context(), scope(cur), id)
	if s.deviceFailed(w, "revoke device", err) {
		return
	}
	s.Log.Info("panel: device revoked", "user", cur.User.Username, "device_id", id)
	w.WriteHeader(http.StatusNoContent)
}

// deviceFailed отвечает на ошибку операции с устройством. Устройство вне
// области владельца сессии приходит как ErrNotFound и неотличимо от несуществующего.
func (s *Server) deviceFailed(w http.ResponseWriter, op string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, accounts.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user_not_found", err.Error())
	case errors.Is(err, accounts.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, accounts.ErrDeviceExists):
		writeError(w, http.StatusConflict, "device_exists", err.Error())
	case errors.Is(err, accounts.ErrInvalidDeviceName):
		writeError(w, http.StatusBadRequest, "invalid_device_name", err.Error())
	default:
		s.internal(w, op, err)
	}
	return true
}

func device(d accounts.DeviceInfo) Device {
	return Device{
		Id:             d.ID,
		Name:           d.Name,
		Owner:          d.Owner,
		CreatedAt:      d.CreatedAt.UTC(),
		LastUsedAt:     utc(d.LastUsedAt),
		RevokedAt:      utc(d.RevokedAt),
		ReportedDevice: d.ReportedDevice,
	}
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
