package opds

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library"
	"github.com/varrcan/koreader-sync-board/server/internal/library/convert"
	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

const (
	// downloadTimeout заменяет общий срок записи сервера (30 с): книга в сотни
	// мегабайт по Wi-Fi читалки идёт дольше, а обрыв докачивается через Range.
	downloadTimeout = 30 * time.Minute
	// convertResponseTimeout — ответ ждёт конвертацию.
	convertResponseTimeout = convert.Timeout + time.Minute
)

func fileID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) file(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.serveFile(w, r, id)
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, id int64) {
	loc, err := h.lib.Store.FileLocation(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrFileNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.internal(w, r, "file location", err)
		return
	}
	f, fi, err := library.OpenFile(loc)
	if err != nil {
		h.log.Warn("opds: library file unavailable", "root", loc.Root, "path", loc.Path, "err", err)
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	// ResponseRecorder сроков не поддерживает, ошибка не важна.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadTimeout))
	name := path.Base(loc.Path)
	header := w.Header()
	header.Set("Content-Type", formatType(loc.Format))
	header.Set("Content-Disposition", disposition(name))
	header.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// disposition дополняет кириллическое имя вариантом filename="…": getServerFileName
// в KOReader ищет только filename="…" и filename=…, а filename* ему не виден. Значение
// он сам пропускает через util.urlDecode, клиенты по RFC 6266 предпочитают filename*.
func disposition(name string) string {
	v := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if strings.Contains(v, "filename*=") {
		v += `; filename="` + url.PathEscape(name) + `"`
	}
	return v
}

// epub отдаёт EPUB из FB2, создавая его при первом запросе. Клиент, не дождавшийся
// ответа (таймаут KOReader — 15 с), конвертацию не отменяет: следующий запрос получит
// готовый файл.
func (h *Handler) epub(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodHead {
		h.epubHead(w, r, id)
		return
	}
	// ResponseRecorder сроков не поддерживает, ошибка не важна.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(convertResponseTimeout))
	result, err := h.lib.Converter.Convert(r.Context(), id)
	switch {
	case err == nil:
		h.serveFile(w, r, result)
	case r.Context().Err() != nil:
	case errors.Is(err, library.ErrFileNotFound), errors.Is(err, convert.ErrUnavailable), errors.Is(err, convert.ErrNotConvertible):
		http.NotFound(w, r)
	case errors.Is(err, meta.ErrTooLarge):
		http.Error(w, "file is too large to convert", http.StatusRequestEntityTooLarge)
	case errors.Is(err, convert.ErrTimeout):
		http.Error(w, "conversion timed out", http.StatusGatewayTimeout)
	case errors.Is(err, convert.ErrFailed):
		http.Error(w, "conversion failed", http.StatusUnprocessableEntity)
	default:
		h.internal(w, r, "convert", err)
	}
}

// epubHead отвечает без конвертации. HEAD шлёт только «Использовать имена файлов
// сервера» с таймаутом 10 с и без показа ошибки, а имя результата известно заранее.
func (h *Handler) epubHead(w http.ResponseWriter, r *http.Request, id int64) {
	loc, err := h.lib.Store.FileLocation(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrFileNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.internal(w, r, "file location", err)
		return
	}
	if loc.Format != "fb2" && loc.Format != "fb2.zip" {
		http.NotFound(w, r)
		return
	}
	header := w.Header()
	header.Set("Content-Type", epubType)
	header.Set("Content-Disposition", disposition(convert.ResultName(loc.Path, loc.Format)))
	w.WriteHeader(http.StatusOK)
}

// cover отдаёт миниатюру. Имя — хеш содержимого, поэтому кеш вечный; private
// обязателен: запрос идёт с паролем.
func (h *Handler) cover(w http.ResponseWriter, r *http.Request) {
	cover := r.PathValue("cover")
	f, err := h.lib.Covers.Open(cover)
	switch {
	case errors.Is(err, library.ErrCoverNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.internal(w, r, "open cover", err)
		return
	}
	defer f.Close()
	header := w.Header()
	header.Set("Content-Type", coverType)
	header.Set("ETag", `"`+cover+`"`)
	header.Set("Cache-Control", "private, max-age=31536000, immutable")
	header.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, f)
}
