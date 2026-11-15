package webdav

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"syscall"
	"time"

	xwebdav "golang.org/x/net/webdav"

	"github.com/varrcan/koreader-sync-board/server/internal/uploads"
)

// BooksPrefix — путь WebDAV-диска книг: общая для всех пользователей папка поверх
// корня загрузок.
const BooksPrefix = Prefix + "/books"

// downloadTimeout заменяет общий срок записи сервера (30 с) при скачивании книги,
// как скачивание в панели и OPDS.
const downloadTimeout = 30 * time.Minute

// Books — WebDAV-диск книг. Changed вызывается после успешного изменения папки:
// сервер ставит скан корня загрузок.
type Books struct {
	Dir     *uploads.Dir
	Changed func()
}

// booksHandler обслуживает /dav/books/. PUT свой: x/net/webdav пишет прямо в цель
// (OpenFile с O_TRUNC, затем io.Copy) и закрывает файл даже после ошибки копирования,
// так что оборванная передача заменила бы книгу половиной файла. Остальные методы —
// x/net/webdav: проверенная клиентами реализация класса 2, без которого Finder
// монтирует только для чтения. Блокировки в памяти, свой PUT их не проверяет: кто
// записал последним, тот и прав.
type booksHandler struct {
	dir     *uploads.Dir
	changed func()
	dav     *xwebdav.Handler
	log     *slog.Logger
}

func newBooksHandler(b *Books, log *slog.Logger) *booksHandler {
	return &booksHandler{dir: b.Dir, changed: b.Changed, log: log, dav: &xwebdav.Handler{
		Prefix:     BooksPrefix,
		FileSystem: xwebdav.Dir(b.Dir.Path()),
		LockSystem: xwebdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				log.Debug("webdav: books", "method", r.Method, "path", r.URL.Path, "error", err)
			}
		},
	}}
}

func isBooks(p string) bool {
	return p == BooksPrefix || strings.HasPrefix(p, BooksPrefix+"/")
}

// changes — методы, после успешного ответа на которые содержимое папки другое.
// LOCK может создать пустой файл, но такие сканер пропускает.
var changes = map[string]bool{
	http.MethodPut: true, http.MethodDelete: true, "MKCOL": true, "MOVE": true, "COPY": true,
}

func (h *booksHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		// ResponseRecorder сроков не поддерживает, ошибка не важна. Без обёртки:
		// ServeContent отдаёт файл через ReadFrom (sendfile).
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadTimeout))
		h.dav.ServeHTTP(w, r)
	case !changes[r.Method]:
		h.dav.ServeHTTP(w, r)
	default:
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		if r.Method == http.MethodPut {
			h.put(sw, r)
		} else {
			h.dav.ServeHTTP(sw, r)
		}
		if sw.status >= 200 && sw.status < 300 {
			h.changed()
		}
	}
}

// put пишет тело во временный файл рядом с целью и переименовывает его в цель только
// после полного чтения: оборванная передача не меняет книгу и не оставляет следов.
func (h *booksHandler) put(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, BooksPrefix)), "/")
	if name == "" {
		http.Error(w, "cannot PUT a collection", http.StatusMethodNotAllowed)
		return
	}
	if r.ContentLength > uploads.MaxSize {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	existed := false
	if fi, err := h.dir.Stat(name); err == nil {
		if fi.IsDir() {
			http.Error(w, "cannot PUT a collection", http.StatusMethodNotAllowed)
			return
		}
		existed = true
	}
	tmp, _, err := h.dir.Receive(path.Dir(name), uploads.Body(w, r), uploads.MaxSize)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferTimeout))
	var readErr uploads.ReadError
	switch {
	case err == nil:
	case errors.Is(err, uploads.ErrTooLarge):
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	case errors.As(err, &readErr):
		http.Error(w, "cannot read request body", http.StatusBadRequest)
		return
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		// Как у x/net/webdav: родительской папки нет.
		http.Error(w, "parent collection does not exist", http.StatusConflict)
		return
	default:
		h.internal(w, "books put", err)
		return
	}
	if err := h.dir.Replace(tmp, name); err != nil {
		h.internal(w, "books put", err)
		return
	}
	fi, err := h.dir.Stat(name)
	if err != nil {
		h.internal(w, "books put", err)
		return
	}
	// Формат findETag из x/net/webdav: тот же ETag вернёт PROPFIND.
	w.Header().Set("ETag", fmt.Sprintf(`"%x%x"`, fi.ModTime().UnixNano(), fi.Size()))
	if existed {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

func (h *booksHandler) internal(w http.ResponseWriter, op string, err error) {
	h.log.Error("webdav: "+op, "err", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// statusWriter запоминает код ответа.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(p)
}

// Unwrap нужен http.ResponseController: сроки ставятся на исходном соединении.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
