package apiv1

import (
	"errors"
	"net/http"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
)

// Library — индекс библиотеки и его сканер.
type Library struct {
	Store   *library.Store
	Scanner *library.Scanner
}

func (s *Server) GetLibrary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.librarySummary(r)
	if err != nil {
		s.internal(w, "library summary", err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) ListLibraryRoots(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	roots, err := s.Library.Store.Roots(r.Context())
	if err != nil {
		s.internal(w, "list library roots", err)
		return
	}
	out := LibraryRootList{Items: make([]LibraryRoot, 0, len(roots))}
	for _, root := range roots {
		out.Items = append(out.Items, libraryRoot(root))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CreateLibraryRoot(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req CreateLibraryRootRequest
	if !decode(w, r, &req) {
		return
	}
	var name string
	if req.Name != nil {
		name = *req.Name
	}
	root, err := s.Library.Store.AddRoot(r.Context(), req.Path, name)
	switch {
	case err == nil:
	case errors.Is(err, library.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	case errors.Is(err, library.ErrRootExists):
		writeError(w, http.StatusConflict, "root_exists", err.Error())
		return
	case errors.Is(err, library.ErrRootOverlap):
		writeError(w, http.StatusConflict, "root_overlap", err.Error())
		return
	default:
		s.internal(w, "add library root", err)
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	s.Log.Info("panel: library root added", "user", cur.User.Username, "root", root.Path)
	s.Library.Scanner.Trigger()
	writeJSON(w, http.StatusCreated, libraryRoot(root))
}

func (s *Server) DeleteLibraryRoot(w http.ResponseWriter, r *http.Request, id LibraryRootID) {
	if !requireAdmin(w, r) {
		return
	}
	err := s.Library.Store.DeleteRoot(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	case err != nil:
		s.internal(w, "delete library root", err)
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	s.Log.Info("panel: library root deleted", "user", cur.User.Username, "root_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ScanLibrary(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	s.Library.Scanner.Trigger()
	sum, err := s.librarySummary(r)
	if err != nil {
		s.internal(w, "library summary", err)
		return
	}
	// Скан маленькой библиотеки успевает закончиться до чтения сводки, а клиент
	// по running включает опрос: ответ на запуск всегда показывает запущенный скан.
	sum.Scan.Running = true
	writeJSON(w, http.StatusAccepted, sum)
}

func (s *Server) librarySummary(r *http.Request) (LibrarySummary, error) {
	sum, err := s.Library.Scanner.Summary(r.Context())
	if err != nil {
		return LibrarySummary{}, err
	}
	return LibrarySummary{
		Books: sum.Books, Files: sum.Files, Roots: sum.Roots,
		Scan: LibraryScan{Running: sum.Running, StartedAt: sum.StartedAt, FinishedAt: sum.FinishedAt},
	}, nil
}

// requireAdmin отвечает 403, если владелец сессии не администратор.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	cur, _ := panelauth.FromContext(r.Context())
	if cur.User.Role != accounts.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "administrator role required")
		return false
	}
	return true
}

func libraryRoot(r library.Root) LibraryRoot {
	return LibraryRoot{Id: r.ID, Name: r.Name, Path: r.Path, Files: r.Files, ScannedAt: r.ScannedAt, Error: r.Error}
}
