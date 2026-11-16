package apiv1

import (
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/lookup"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
)

const (
	maxMetadataTitle       = 500
	maxMetadataAuthors     = 1000
	maxMetadataLanguage    = 16
	maxMetadataDescription = 20000
)

var metadataSources = []string{lookup.ProviderFile, lookup.ProviderFantLab, lookup.ProviderOpenLibrary, lookup.ProviderGoogle}

func (s *Server) GetLibraryBookMetadataCandidates(w http.ResponseWriter, r *http.Request, id LibraryBookID, params GetLibraryBookMetadataCandidatesParams) {
	if !requireAdmin(w, r) {
		return
	}
	text, isbn := trimmed(params.Q), trimmed(params.Isbn)
	if utf8.RuneCountInString(text) > maxSearchLength {
		writeError(w, http.StatusBadRequest, "bad_request", "q is too long")
		return
	}
	if isbn != "" {
		if isbn = meta.NormalizeISBN(isbn); isbn == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid isbn")
			return
		}
	}
	cur, _ := panelauth.FromContext(r.Context())
	book, err := s.Library.Store.Book(r.Context(), id, cur.User.ID, true)
	switch {
	case errors.Is(err, library.ErrBookNotFound):
		writeError(w, http.StatusNotFound, "not_found", "book not found")
		return
	case err != nil:
		s.internal(w, "get library book", err)
		return
	}
	files, err := s.Library.Store.ParseableFiles(r.Context(), id)
	if err != nil {
		s.internal(w, "library book files", err)
		return
	}
	fromFiles := s.Library.Files.Read(files)
	// Ручной запрос не дополняется данными файла: иначе ошибочный ISBN в файле нельзя
	// было бы обойти.
	if text == "" && isbn == "" {
		text, isbn = library.AutoQuery(book.Book), fromFiles.ISBN
	}
	res := s.Library.Lookup.Search(r.Context(), lookup.Query{ISBN: isbn, Text: text, Language: library.BookLanguage(book.Book)})

	out := LibraryMetadataCandidates{
		Query: optional(text), Isbn: optional(isbn),
		Items:     make([]LibraryMetadataCandidate, 0, len(res.Items)+1),
		Providers: make([]LibraryMetadataProvider, 0, len(res.Providers)),
	}
	if fromFiles.Title != "" || fromFiles.Description != "" {
		out.Items = append(out.Items, metadataCandidate(fileCandidate(fromFiles)))
	}
	for _, c := range res.Items {
		out.Items = append(out.Items, metadataCandidate(c))
	}
	for _, p := range res.Providers {
		out.Providers = append(out.Providers, LibraryMetadataProvider{Id: p.ID, Status: p.Status})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) PutLibraryBookMetadata(w http.ResponseWriter, r *http.Request, id LibraryBookID) {
	if !requireAdmin(w, r) {
		return
	}
	var req LibraryMetadataForm
	if !decode(w, r, &req) {
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	book, err := s.Library.Store.Book(r.Context(), id, cur.User.ID, true)
	switch {
	case errors.Is(err, library.ErrBookNotFound):
		writeError(w, http.StatusNotFound, "not_found", "book not found")
		return
	case err != nil:
		s.internal(w, "get library book", err)
		return
	}
	edit, msg := metadataEdit(req, book.Book)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_metadata", msg)
		return
	}
	coverURL := trimmed(req.CoverUrl)
	if coverURL != "" && !lookup.ValidCoverURL(coverURL) {
		writeError(w, http.StatusBadRequest, "invalid_metadata", "cover_url is not allowed")
		return
	}
	if coverURL == "" {
		err = s.Library.Store.SetMetadata(r.Context(), id, edit)
	} else {
		// Сеть и декодирование — до транзакции: писатель общий с kosync.
		var thumb []byte
		thumb, err = s.coverThumbnail(r, coverURL)
		if err == nil {
			err = s.Library.Covers.Attach(thumb, func(cover string) error {
				edit.Cover = &cover
				return s.Library.Store.SetMetadata(r.Context(), id, edit)
			})
		}
	}
	switch {
	case errors.Is(err, library.ErrBookNotFound):
		writeError(w, http.StatusNotFound, "not_found", "book not found")
		return
	case errors.Is(err, lookup.ErrCoverURL):
		writeError(w, http.StatusBadRequest, "invalid_metadata", "cover_url is not allowed")
		return
	case errors.Is(err, lookup.ErrCoverUnavailable):
		writeError(w, http.StatusBadGateway, "cover_unavailable", "cover image is unavailable")
		return
	case err != nil:
		s.internal(w, "save library metadata", err)
		return
	}
	s.Log.Info("panel: library metadata saved", "user", cur.User.Username, "book", id)
	s.writeLibraryBook(w, r, http.StatusOK, id)
}

// coverThumbnail скачивает изображение и строит миниатюру; изображение, которое не
// удалось декодировать, для клиента то же, что недоступное.
func (s *Server) coverThumbnail(r *http.Request, url string) ([]byte, error) {
	src, err := s.Library.CoverSource.Fetch(r.Context(), url)
	if err != nil {
		return nil, err
	}
	thumb, err := library.Thumbnail(src)
	if err != nil {
		return nil, errors.Join(lookup.ErrCoverUnavailable, err)
	}
	return thumb, nil
}

func (s *Server) DeleteLibraryBookMetadata(w http.ResponseWriter, r *http.Request, id LibraryBookID) {
	if !requireAdmin(w, r) {
		return
	}
	err := s.Library.Store.ClearMetadata(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrBookNotFound):
		writeError(w, http.StatusNotFound, "not_found", "book not found")
		return
	case err != nil:
		s.internal(w, "clear library metadata", err)
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	s.Log.Info("panel: library metadata cleared", "user", cur.User.Username, "book", id)
	s.writeLibraryBook(w, r, http.StatusOK, id)
}

func (s *Server) DismissLibraryBookSuggestion(w http.ResponseWriter, r *http.Request, id LibraryBookID) {
	if !requireAdmin(w, r) {
		return
	}
	err := s.Library.Store.DismissSuggestion(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrBookNotFound):
		writeError(w, http.StatusNotFound, "not_found", "book not found")
		return
	case err != nil:
		s.internal(w, "dismiss library suggestion", err)
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	s.Log.Info("panel: library suggestion dismissed", "user", cur.User.Username, "book", id)
	w.WriteHeader(http.StatusNoContent)
}

// metadataEdit проверяет форму; непустая строка — причина отказа. Значения книги
// пределам не подчиняются: файлы не ограничены, а форма присылает все поля, и
// длинный список авторов антологии иначе не дал бы исправить даже серию.
func metadataEdit(req LibraryMetadataForm, book library.Book) (library.MetadataEdit, string) {
	e := library.MetadataEdit{
		Title: trimmed(req.Title), Authors: trimmed(req.Authors), Series: trimmed(req.Series),
		Language: trimmed(req.Language), Description: trimmed(req.Description), SeriesIndex: req.SeriesIndex,
	}
	for _, f := range []struct {
		name, value string
		current     *string
		limit       int
	}{
		{"title", e.Title, book.Title, maxMetadataTitle},
		{"authors", e.Authors, book.Authors, maxMetadataAuthors},
		{"series", e.Series, book.Series, maxMetadataTitle},
		{"language", e.Language, book.Language, maxMetadataLanguage},
		{"description", e.Description, nil, maxMetadataDescription},
	} {
		if utf8.RuneCountInString(f.value) > f.limit && (f.current == nil || *f.current != f.value) {
			return e, f.name + " is too long"
		}
	}
	unchanged := e.SeriesIndex != nil && book.SeriesIndex != nil && *e.SeriesIndex == *book.SeriesIndex
	if i := e.SeriesIndex; i != nil && !unchanged && (*i < 0 || math.IsInf(*i, 0) || math.IsNaN(*i)) {
		return e, "series_index must be a finite non-negative number"
	}
	if req.Source != nil {
		if !slices.Contains(metadataSources, *req.Source) {
			return e, "unknown source"
		}
		e.Source = req.Source
	}
	e.ResetCover = req.ResetCover != nil && *req.ResetCover
	if e.ResetCover && trimmed(req.CoverUrl) != "" {
		return e, "cover_url and reset_cover are mutually exclusive"
	}
	return e, ""
}

func trimmed(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func fileCandidate(b meta.Book) lookup.Candidate {
	return lookup.Clamp(lookup.Candidate{
		Provider: lookup.ProviderFile, Match: lookup.MatchFile, Title: b.Title, Authors: strings.Join(b.Authors, ", "),
		Series: b.Series, SeriesIndex: b.SeriesIndex, Language: b.Language, Description: b.Description, ISBN: b.ISBN,
	})
}

func metadataCandidate(c lookup.Candidate) LibraryMetadataCandidate {
	return LibraryMetadataCandidate{
		Provider: c.Provider, Match: c.Match, Title: optional(c.Title),
		Authors: optional(c.Authors), Series: optional(c.Series), SeriesIndex: c.SeriesIndex,
		Language: optional(c.Language), Year: c.Year, Description: optional(c.Description),
		Isbn: optional(c.ISBN), CoverUrl: optional(c.CoverURL), Url: optional(c.URL),
	}
}
