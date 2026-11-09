package apiv1

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/convert"
	"github.com/varrcan/koreader-sync-board/server/internal/library/lookup"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
)

const (
	maxSearchLength = 200
	// downloadTimeout заменяет общий срок записи сервера (30 с): книга в сотни
	// мегабайт по каналу ~1 Мбит/с укладывается, а зависший клиент не держит
	// соединение вечно. Обрыв докачивается через Range.
	downloadTimeout = 30 * time.Minute
	// convertResponseTimeout заменяет общий срок записи: ответ ждёт конвертацию.
	convertResponseTimeout = convert.Timeout + time.Minute
)

// contentTypes — Content-Type файла по формату индекса.
var contentTypes = map[string]string{
	"epub":    "application/epub+zip",
	"fb2":     "application/x-fictionbook+xml",
	"fb2.zip": "application/zip",
	"pdf":     "application/pdf",
	"djvu":    "image/vnd.djvu",
	"mobi":    "application/x-mobipocket-ebook",
	"azw":     "application/x-mobipocket-ebook",
	"cbz":     "application/vnd.comicbook+zip",
}

// Library — индекс библиотеки и его сканер.
type Library struct {
	Store       *library.Store
	Scanner     *library.Scanner
	Covers      *library.Covers
	Files       library.FileReader
	Lookup      MetadataLookup
	CoverSource CoverSource
	Converter   *convert.Service
}

// MetadataLookup ищет кандидатов во внешних каталогах.
type MetadataLookup interface {
	Search(ctx context.Context, q lookup.Query) lookup.Result
}

// CoverSource скачивает обложку кандидата.
type CoverSource interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
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

func (s *Server) ListLibraryBooks(w http.ResponseWriter, r *http.Request, params ListLibraryBooksParams) {
	limit, before, ok := pageParams(w, params.Limit, params.Cursor)
	if !ok {
		return
	}
	var q string
	if params.Q != nil {
		q = *params.Q
	}
	if utf8.RuneCountInString(q) > maxSearchLength {
		writeError(w, http.StatusBadRequest, "bad_request", "q is too long")
		return
	}
	p, err := s.Library.Store.Books(r.Context(), q, before, limit)
	if err != nil {
		s.internal(w, "list library books", err)
		return
	}
	out := LibraryBookPage{Items: make([]LibraryBook, 0, len(p.Items)), NextCursor: cursor(p.Next)}
	for _, b := range p.Items {
		out.Items = append(out.Items, libraryBook(b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetLibraryBook(w http.ResponseWriter, r *http.Request, id LibraryBookID) {
	s.writeLibraryBook(w, r, http.StatusOK, id)
}

func (s *Server) MergeLibraryBooks(w http.ResponseWriter, r *http.Request, id LibraryBookID) {
	if !requireAdmin(w, r) {
		return
	}
	var req MergeLibraryBooksRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.Library.Store.Merge(r.Context(), id, req.BookId)
	switch {
	case errors.Is(err, library.ErrSameBook):
		writeError(w, http.StatusBadRequest, "bad_request", "cannot merge a book into itself")
		return
	case errors.Is(err, library.ErrBookNotFound):
		writeError(w, http.StatusNotFound, "not_found", "book not found")
		return
	case err != nil:
		s.internal(w, "merge library books", err)
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	s.Log.Info("panel: library books merged", "user", cur.User.Username, "book", id, "from", req.BookId)
	s.writeLibraryBook(w, r, http.StatusOK, id)
}

func (s *Server) SplitLibraryFile(w http.ResponseWriter, r *http.Request, id LibraryFileID) {
	if !requireAdmin(w, r) {
		return
	}
	book, err := s.Library.Store.Split(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrFileNotFound):
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	case errors.Is(err, library.ErrSingleFile):
		writeError(w, http.StatusConflict, "single_file", "file is the only one in its book")
		return
	case err != nil:
		s.internal(w, "split library file", err)
		return
	}
	cur, _ := panelauth.FromContext(r.Context())
	s.Log.Info("panel: library file split", "user", cur.User.Username, "file", id, "book", book)
	s.writeLibraryBook(w, r, http.StatusCreated, book)
}

// ConvertLibraryFile ждёт результат. Отключение клиента конвертацию не прерывает, и
// следующий запрос получит готовый файл.
func (s *Server) ConvertLibraryFile(w http.ResponseWriter, r *http.Request, id LibraryFileID) {
	// ResponseRecorder сроков не поддерживает, ошибка не важна.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(convertResponseTimeout))
	file, err := s.Library.Converter.Convert(r.Context(), id)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, LibraryConvertResult{FileId: file})
	case r.Context().Err() != nil:
	case errors.Is(err, library.ErrFileNotFound):
		writeError(w, http.StatusNotFound, "not_found", "file not found")
	case errors.Is(err, convert.ErrUnavailable):
		writeError(w, http.StatusNotFound, "file_unavailable", "file cannot be opened")
	case errors.Is(err, convert.ErrNotConvertible):
		writeError(w, http.StatusConflict, "not_convertible", "only FB2 files can be converted")
	case errors.Is(err, meta.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "file is too large to convert")
	case errors.Is(err, convert.ErrTimeout):
		writeError(w, http.StatusGatewayTimeout, "conversion_timeout", "conversion timed out")
	case errors.Is(err, convert.ErrFailed):
		writeError(w, http.StatusUnprocessableEntity, "conversion_failed", "conversion failed")
	default:
		s.internal(w, "convert library file", err)
	}
}

// DeleteLibraryFile удаляет результат конвертации. Запрос ждёт идущую конвертацию.
func (s *Server) DeleteLibraryFile(w http.ResponseWriter, r *http.Request, id LibraryFileID) {
	if !requireAdmin(w, r) {
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(convertResponseTimeout))
	err := s.Library.Converter.Delete(r.Context(), id)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case r.Context().Err() != nil:
	case errors.Is(err, library.ErrFileNotFound):
		writeError(w, http.StatusNotFound, "not_found", "file not found")
	case errors.Is(err, library.ErrNotConverted):
		writeError(w, http.StatusConflict, "not_converted", "file is not a conversion result")
	default:
		s.internal(w, "delete library file", err)
	}
}

func (s *Server) writeLibraryBook(w http.ResponseWriter, r *http.Request, status int, id int64) {
	cur, _ := panelauth.FromContext(r.Context())
	d, err := s.Library.Store.Book(r.Context(), id, cur.User.ID)
	switch {
	case errors.Is(err, library.ErrBookNotFound):
		writeError(w, http.StatusNotFound, "not_found", "book not found")
		return
	case err != nil:
		s.internal(w, "get library book", err)
		return
	}
	out := LibraryBookDetail{Book: libraryBook(d.Book), Files: make([]LibraryFile, 0, len(d.Files)), Description: d.Description}
	if m := d.Metadata; m != nil {
		out.Metadata = &LibraryBookMetadataInfo{Source: m.Source, UpdatedAt: m.UpdatedAt}
	}
	for _, f := range d.Files {
		out.Files = append(out.Files, libraryFile(f))
	}
	writeJSON(w, status, out)
}

// GetLibraryCover отдаёт миниатюру. Имя — хеш содержимого, поэтому кеш вечный;
// private обязателен: запрос идёт с cookie сессии.
func (s *Server) GetLibraryCover(w http.ResponseWriter, r *http.Request, cover string) {
	f, err := s.Library.Covers.Open(cover)
	switch {
	case errors.Is(err, library.ErrCoverNotFound):
		writeError(w, http.StatusNotFound, "not_found", "cover not found")
		return
	case err != nil:
		s.internal(w, "open library cover", err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("ETag", `"`+cover+`"`)
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// GetLibraryFileContent отдаёт файл с диска.
func (s *Server) GetLibraryFileContent(w http.ResponseWriter, r *http.Request, id LibraryFileID) {
	loc, err := s.Library.Store.FileLocation(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrFileNotFound):
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	case err != nil:
		s.internal(w, "library file location", err)
		return
	}
	f, fi, err := library.OpenFile(loc)
	if err != nil {
		s.Log.Warn("panel: library file unavailable", "root", loc.Root, "path", loc.Path, "err", err)
		writeError(w, http.StatusNotFound, "file_unavailable", "file is unavailable")
		return
	}
	defer f.Close()
	// ResponseRecorder сроков не поддерживает, ошибка не важна.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadTimeout))
	name := filepath.Base(filepath.FromSlash(loc.Path))
	h := w.Header()
	h.Set("Content-Type", contentType(loc.Format))
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func contentType(format string) string {
	if t, ok := contentTypes[format]; ok {
		return t
	}
	return "application/octet-stream"
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

func libraryBook(b library.Book) LibraryBook {
	return LibraryBook{
		Id: b.ID, Title: b.Title, Authors: b.Authors, Series: b.Series, SeriesIndex: b.SeriesIndex,
		Language: b.Language, Files: b.Files, Formats: b.Formats, Filename: b.Filename, Cover: b.Cover,
	}
}

func libraryFile(f library.File) LibraryFile {
	out := LibraryFile{
		Id: f.ID, RootName: f.RootName, Path: f.Path, Format: f.Format, Size: f.Size,
		Title: f.Title, Authors: f.Authors, Pinned: f.Pinned, Converted: f.Converted, Convertible: f.Convertible,
	}
	if p := f.Progress; p != nil {
		out.Progress = &LibraryFileProgress{
			Hash: p.Hash, Percentage: p.Percentage, Finished: p.Finished, UpdatedAt: p.UpdatedAt,
		}
	}
	return out
}
