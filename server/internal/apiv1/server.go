package apiv1

import (
	"encoding/json"
	"net/http"

	"github.com/varrcan/koreader-sync-board/server/internal/buildinfo"
)

// Server реализует ServerInterface.
type Server struct{}

var _ ServerInterface = Server{}

func (Server) GetHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Health{Status: Ok})
}

func (Server) GetVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Version{Version: buildinfo.Version, Commit: buildinfo.CommitOrVCS()})
}

// NewHandler возвращает обработчик всего префикса /api/. Неизвестный путь
// получает JSON 404, а не index.html.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, Error{Code: "not_found", Message: "unknown API endpoint"})
	})
	return HandlerWithOptions(Server{}, StdHTTPServerOptions{
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSON(w, http.StatusBadRequest, Error{Code: "bad_request", Message: err.Error()})
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
