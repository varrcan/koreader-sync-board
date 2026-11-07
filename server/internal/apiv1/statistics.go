package apiv1

import (
	"errors"
	"io"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

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

func (s *Server) GetStatisticsSummary(w http.ResponseWriter, r *http.Request, params GetStatisticsSummaryParams) {
	cur, _ := panelauth.FromContext(r.Context())
	// oapi-codegen std-http значения enum не проверяет.
	if !statistics.ValidPeriod(statistics.Period(params.Period)) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid period")
		return
	}
	loc, ok := timeZone(w, params.Tz)
	if !ok {
		return
	}
	sum, err := s.Statistics.Summary(r.Context(), cur.User.ID, statistics.Period(params.Period), loc, time.Now())
	if err != nil {
		s.internal(w, "statistics summary", err)
		return
	}
	out := StatisticsSummary{
		Period:   params.Period,
		From:     day(sum.From),
		To:       day(sum.To),
		Bucket:   StatisticsSummaryBucketDay,
		AsOf:     sum.AsOf,
		Totals:   totals(sum.Totals),
		Previous: totals(sum.Previous),
		Series:   make([]StatisticsBucket, 0, len(sum.Series)),
		Hours:    sum.Hours[:],
		Books:    make([]StatisticsBook, 0, len(sum.Books)),
	}
	if sum.Monthly {
		out.Bucket = StatisticsSummaryBucketMonth
	}
	for _, b := range sum.Series {
		out.Series = append(out.Series, StatisticsBucket{Date: day(b.Date), Duration: b.Duration, Pages: b.Pages, NoData: b.NoData})
	}
	for _, b := range sum.Books {
		out.Books = append(out.Books, StatisticsBook{
			Title: b.Title, Authors: b.Authors, Hash: b.Hash, HasProgress: b.HasProgress,
			Duration: b.Duration, Pages: b.Pages,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetStatisticsCalendar(w http.ResponseWriter, r *http.Request, params GetStatisticsCalendarParams) {
	cur, _ := panelauth.FromContext(r.Context())
	loc, ok := timeZone(w, params.Tz)
	if !ok {
		return
	}
	cal, err := s.Statistics.Calendar(r.Context(), cur.User.ID, loc, time.Now())
	if err != nil {
		s.internal(w, "statistics calendar", err)
		return
	}
	out := StatisticsCalendar{From: day(cal.From), To: day(cal.To), AsOf: cal.AsOf, Days: make([]StatisticsDay, 0, len(cal.Days))}
	for _, d := range cal.Days {
		out.Days = append(out.Days, StatisticsDay{Date: day(d.Date), Duration: d.Duration})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ListReadingSessions(w http.ResponseWriter, r *http.Request, params ListReadingSessionsParams) {
	cur, _ := panelauth.FromContext(r.Context())
	limit, _, ok := pageParams(w, params.Limit, nil)
	if !ok {
		return
	}
	var after *statistics.SessionCursor
	if params.Cursor != nil {
		c, err := statistics.ParseSessionCursor(*params.Cursor)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor")
			return
		}
		after = &c
	}
	items, next, err := s.Statistics.Sessions(r.Context(), cur.User.ID, params.Document, after, limit)
	if err != nil {
		s.internal(w, "reading sessions", err)
		return
	}
	out := ReadingSessionPage{Items: make([]ReadingSession, 0, len(items))}
	if next != nil {
		c := next.String()
		out.NextCursor = &c
	}
	for _, ses := range items {
		out.Items = append(out.Items, ReadingSession{
			Book:      bookRef(ses.Book),
			StartedAt: ses.StartedAt,
			EndedAt:   ses.EndedAt,
			Duration:  ses.Duration,
			Pages:     ses.Pages,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetBookStatistics — статистика книги из снимка и изменение процента за сегодня.
// Видимость — как у карточки книги: без прогресса 404.
func (s *Server) GetBookStatistics(w http.ResponseWriter, r *http.Request, hash BookHash, params GetBookStatisticsParams) {
	cur, _ := panelauth.FromContext(r.Context())
	loc, ok := timeZone(w, params.Tz)
	if !ok {
		return
	}
	b, _, err := s.Progress.Book(r.Context(), cur.User.ID, hash)
	if s.progressFailed(w, "book statistics", err) {
		return
	}
	y, m, d := time.Now().In(loc).Date()
	today, err := s.Progress.Today(r.Context(), cur.User.ID, hash, time.Date(y, m, d, 0, 0, 0, 0, loc))
	if s.progressFailed(w, "book statistics", err) {
		return
	}
	asOf, st, err := s.Statistics.Book(r.Context(), cur.User.ID, hash, b.Percentage, loc)
	if err != nil {
		s.internal(w, "book statistics", err)
		return
	}
	out := BookStatistics{AsOf: asOf, PercentageToday: today}
	if st != nil {
		out.Stats = &BookReadingStats{
			Duration:    st.Duration,
			PagesRead:   st.PagesRead,
			Days:        st.Days,
			Sessions:    st.Sessions,
			FirstReadAt: st.FirstReadAt,
			LastReadAt:  st.LastReadAt,
			PageTime:    st.PageTime,
			Pages:       st.Pages,
			CurrentPage: st.CurrentPage,
			TimeLeft:    st.TimeLeft,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// timeZone разбирает пояс IANA. Пустая строка и Local отвергаются: LoadLocation
// отдаёт для них UTC и пояс сервера, а не пользователя.
func timeZone(w http.ResponseWriter, name string) (*time.Location, bool) {
	if name == "" || name == "Local" {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid tz")
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid tz")
		return nil, false
	}
	return loc, true
}

func day(t time.Time) openapi_types.Date {
	return openapi_types.Date{Time: t}
}

func totals(t statistics.Totals) StatisticsTotals {
	return StatisticsTotals{Duration: t.Duration, Pages: t.Pages, Sessions: t.Sessions, Days: t.Days}
}

func bookRef(b statistics.BookRef) StatisticsBookRef {
	return StatisticsBookRef{Title: b.Title, Authors: b.Authors, Hash: b.Hash, HasProgress: b.HasProgress}
}
