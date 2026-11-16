package apiv1

import (
	"net/http"

	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
)

func (s *Server) GetSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	s.writeSettings(w, r)
}

func (s *Server) PutSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req SettingsForm
	if !decode(w, r, &req) {
		return
	}
	if v := req.LibraryMetadataSuggest; v != nil {
		if err := s.Settings.SetMetadataSuggest(r.Context(), *v); err != nil {
			s.internal(w, "save settings", err)
			return
		}
		cur, _ := panelauth.FromContext(r.Context())
		s.Log.Info("panel: settings changed", "user", cur.User.Username, "library_metadata_suggest", *v)
	}
	s.writeSettings(w, r)
}

func (s *Server) writeSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	suggest, err := s.Settings.MetadataSuggest(ctx)
	if err != nil {
		s.internal(w, "read settings", err)
		return
	}
	pending, err := s.Library.Store.SuggestPending(ctx)
	if err != nil {
		s.internal(w, "metadata suggestions progress", err)
		return
	}
	found, err := s.Library.Store.SuggestFound(ctx)
	if err != nil {
		s.internal(w, "metadata suggestions progress", err)
		return
	}
	writeJSON(w, http.StatusOK, Settings{
		LibraryMetadataSuggest: suggest,
		MetadataSuggestions:    MetadataSuggestionProgress{Pending: pending, Found: found},
	})
}
