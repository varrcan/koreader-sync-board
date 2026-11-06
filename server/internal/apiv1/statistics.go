package apiv1

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics"
)

// importTimeout — сроки чтения и записи соединения для загрузки файла, как в
// WebDAV: сроки сервера (30 с) рассчитаны на мелкие запросы.
const importTimeout = 5 * time.Minute

// Статистика, как и прогресс, всегда читается по владельцу сессии.

func (s *Server) GetStatistics(w http.ResponseWriter, r *http.Request) {
	cur, _ := panelauth.FromContext(r.Context())
	s.writeStatistics(w, r, cur.User.ID)
}

func (s *Server) ImportStatistics(w http.ResponseWriter, r *http.Request) {
	cur, _ := panelauth.FromContext(r.Context())
	if r.ContentLength > statistics.MaxSize {
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "file is larger than 64 MiB")
		return
	}
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(importTimeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, statistics.MaxSize))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "file is larger than 64 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "cannot read request body")
		return
	}
	_, err = s.Statistics.Import(r.Context(), cur.User.ID, body)
	switch {
	case err == nil:
		s.Log.Info("panel: statistics imported", "user", cur.User.Username, "size", len(body))
		s.writeStatistics(w, r, cur.User.ID)
	case errors.Is(err, statistics.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_statistics", err.Error())
	case errors.Is(err, statistics.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		s.internal(w, "import statistics", err)
	}
}

func (s *Server) writeStatistics(w http.ResponseWriter, r *http.Request, userID int64) {
	st, err := s.Statistics.Status(r.Context(), userID)
	if err != nil {
		s.internal(w, "statistics status", err)
		return
	}
	out := StatisticsState{}
	if st != nil {
		out.Snapshot = &StatisticsSnapshot{
			UploadedAt:  st.UploadedAt,
			DeviceName:  st.DeviceName,
			Source:      StatisticsSource(st.Source),
			Size:        st.Size,
			Books:       st.Books,
			Records:     st.Records,
			FirstReadAt: st.FirstReadAt,
			LastReadAt:  st.LastReadAt,
		}
	}
	writeJSON(w, http.StatusOK, out)
}
