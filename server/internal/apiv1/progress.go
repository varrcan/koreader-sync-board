package apiv1

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
)

const defaultLimit = 50

// Прогресс всегда читается по владельцу сессии, в том числе администратором:
// данные чтения у каждого свои. Чужая книга не находится тем же запросом, что
// и несуществующая.

func (s *Server) ListBooks(w http.ResponseWriter, r *http.Request, params ListBooksParams) {
	cur, _ := panelauth.FromContext(r.Context())
	f := progress.FilterAll
	if params.Filter != nil {
		if !params.Filter.Valid() {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid filter")
			return
		}
		f = progress.Filter(*params.Filter)
	}
	limit, before, ok := pageParams(w, params.Limit, params.Cursor)
	if !ok {
		return
	}
	p, err := s.Progress.Books(r.Context(), cur.User.ID, f, before, limit)
	if err != nil {
		s.internal(w, "list books", err)
		return
	}
	out := BookPage{
		Items:      make([]Book, 0, len(p.Items)),
		NextCursor: cursor(p.Next),
		Counts: BookCounts{
			All: p.Counts.All, Reading: p.Counts.Reading, Finished: p.Counts.Finished, Unknown: p.Counts.Unknown,
		},
	}
	for _, b := range p.Items {
		out.Items = append(out.Items, book(b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetBook(w http.ResponseWriter, r *http.Request, hash BookHash) {
	cur, _ := panelauth.FromContext(r.Context())
	b, ds, err := s.Progress.Book(r.Context(), cur.User.ID, hash)
	if s.progressFailed(w, "get book", err) {
		return
	}
	out := BookDetail{Book: book(b), Devices: make([]BookDevice, 0, len(ds))}
	for _, d := range ds {
		out.Devices = append(out.Devices, BookDevice{
			DeviceName: d.DeviceName, ReportedDevice: d.ReportedDevice, Percentage: d.Percentage, UpdatedAt: d.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ListActivity(w http.ResponseWriter, r *http.Request, params ListActivityParams) {
	cur, _ := panelauth.FromContext(r.Context())
	limit, before, ok := pageParams(w, params.Limit, params.Cursor)
	if !ok {
		return
	}
	var doc string
	if params.Document != nil {
		// Пустой хеш означал бы всю ленту под видом ленты одной книги.
		if *params.Document == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "empty document")
			return
		}
		doc = *params.Document
	}
	p, err := s.Progress.Activity(r.Context(), cur.User.ID, doc, before, limit)
	if s.progressFailed(w, "list activity", err) {
		return
	}
	out := ActivityPage{Items: make([]ActivityItem, 0, len(p.Items)), NextCursor: cursor(p.Next)}
	for _, g := range p.Items {
		out.Items = append(out.Items, ActivityItem{
			Hash: g.Hash, Title: g.Title, Authors: g.Authors, Filename: g.Filename,
			DeviceName: g.DeviceName, ReportedDevice: g.ReportedDevice,
			FromPercentage: g.FromPercentage, ToPercentage: g.ToPercentage,
			StartedAt: g.StartedAt, EndedAt: g.EndedAt, Syncs: g.Syncs,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// pageParams проверяет limit и разбирает курсор — десятичный id, выданный
// предыдущей страницей. При ошибке отвечает 400 bad_request.
func pageParams(w http.ResponseWriter, limit *Limit, c *Cursor) (int, int64, bool) {
	n := defaultLimit
	if limit != nil {
		n = *limit
	}
	if n < 1 || n > 100 {
		writeError(w, http.StatusBadRequest, "bad_request", "limit must be 1–100")
		return 0, 0, false
	}
	var before int64
	if c != nil {
		v, err := strconv.ParseInt(*c, 10, 64)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor")
			return 0, 0, false
		}
		before = v
	}
	return n, before, true
}

func cursor(next int64) *string {
	if next == 0 {
		return nil
	}
	c := strconv.FormatInt(next, 10)
	return &c
}

func (s *Server) progressFailed(w http.ResponseWriter, op string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, progress.ErrNoProgress):
		writeError(w, http.StatusNotFound, "not_found", "book not found")
	default:
		s.internal(w, op, err)
	}
	return true
}

func book(b progress.Book) Book {
	return Book{
		Hash: b.Hash, Title: b.Title, Authors: b.Authors, Filename: b.Filename,
		Percentage: b.Percentage, Finished: b.Finished, UpdatedAt: b.UpdatedAt,
		DeviceName: b.DeviceName, ReportedDevice: b.ReportedDevice, LibraryBookId: b.LibraryBookID,
	}
}
