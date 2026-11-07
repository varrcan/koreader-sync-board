package statistics

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SessionGap — наибольшая пауза внутри сессии: от начала записи до наибольшего
// конца предыдущих записей сессии. KOReader обрезает время страницы до max_sec
// (по умолчанию 120 с), поэтому страница, открытая 10 минут, даёт запись 120 с и
// паузу около 8 минут до следующей; 10 минут отделяют «отвлёкся» от «закрыл книгу».
const SessionGap = 600

// maxPagesPerRecord — предел размножения записи: вьюха page_stat KOReader берёт
// индексы из таблицы numbers, а в ней числа 1–1000.
const maxPagesPerRecord = 1000

// topBooks — сколько книг в топе периода.
const topBooks = 10

// Period — период итогов.
type Period string

const (
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
	PeriodYear  Period = "year"
)

// ValidPeriod сообщает, известен ли период.
func ValidPeriod(p Period) bool {
	return p == PeriodWeek || p == PeriodMonth || p == PeriodYear
}

// BookRef — книга агрегатов. Hash == nil у книги статистики без md5.
type BookRef struct {
	Title, Authors, Hash *string
	HasProgress          bool
}

// Totals — итоги периода.
type Totals struct {
	Duration              int64
	Pages, Sessions, Days int
}

// Bucket — день или месяц ряда. Date — полночь UTC той же календарной даты.
type Bucket struct {
	Date     time.Time
	Duration int64
	Pages    int
	NoData   bool
}

// BookTotal — книга топа периода.
type BookTotal struct {
	BookRef
	Duration int64
	Pages    int
}

// Summary — итоги периода. Даты — полночь UTC календарных дат пояса запроса.
type Summary struct {
	Period           Period
	From, To         time.Time
	Monthly          bool
	AsOf             *time.Time
	Totals, Previous Totals
	Series           []Bucket
	Hours            [24]int64
	Books            []BookTotal
}

// Day — день календаря с чтением.
type Day struct {
	Date     time.Time
	Duration int64
}

// Calendar — время чтения по дням за 53 недели.
type Calendar struct {
	From, To time.Time
	AsOf     *time.Time
	Days     []Day
}

// Session — сессия чтения.
type Session struct {
	Book               BookRef
	StartedAt, EndedAt time.Time
	Duration           int64
	Pages              int

	key int64
}

// SessionCursor — позиция в списке сессий: начало и ключ книги последней отданной
// сессии. Пара нужна, потому что при слиянии читалок сессии разных книг могут
// начаться в одну секунду. Начало может быть нулевым и отрицательным: start_time в
// KOReader — NOT NULL DEFAULT 0, а часы читалки бывают сбиты.
type SessionCursor struct {
	Start, Book int64
}

// ErrInvalidCursor — курсор сессий не разобран.
var ErrInvalidCursor = errors.New("invalid cursor")

func (c SessionCursor) String() string {
	return strconv.FormatInt(c.Start, 10) + "." + strconv.FormatInt(c.Book, 10)
}

// ParseSessionCursor разбирает курсор вида <unix>.<ключ книги>.
func ParseSessionCursor(s string) (SessionCursor, error) {
	start, book, ok := strings.Cut(s, ".")
	if !ok {
		return SessionCursor{}, ErrInvalidCursor
	}
	var c SessionCursor
	var err1, err2 error
	c.Start, err1 = strconv.ParseInt(start, 10, 64)
	c.Book, err2 = strconv.ParseInt(book, 10, 64)
	if err1 != nil || err2 != nil || c.Book == 0 {
		return SessionCursor{}, ErrInvalidCursor
	}
	return c, nil
}

// before сообщает, идёт ли сессия после курсора в порядке убывания; nil — с начала.
func (c *SessionCursor) before(s *Session) bool {
	if c == nil {
		return true
	}
	start := s.StartedAt.Unix()
	return start < c.Start || start == c.Start && s.key < c.Book
}

// record — запись чтения с пересчитанными страницами [first, last].
type record struct {
	key             int64
	start, duration int64
	first, last     int64
}

// report — всё, из чего считаются агрегаты пользователя.
type report struct {
	asOf    *time.Time
	books   map[int64]BookRef
	records []record
}

// load читает снимок пользователя в одной read-транзакции.
//
// Ключ книги — document_id, у строки без хеша — минус её id: в KOReader строки
// book уникальны по title, authors и md5, и после правки метаданных у хеша
// бывает несколько строк, а это одна книга.
func (s *Store) load(ctx context.Context, userID int64) (*report, error) {
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rep := &report{books: map[int64]BookRef{}}
	var asOf int64
	err = tx.QueryRowContext(ctx, `SELECT modified_at FROM webdav_files WHERE user_id = ? AND name = ?`,
		userID, FileStatistics).Scan(&asOf)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		t := time.Unix(asOf, 0).UTC()
		rep.asOf = &t
	}

	// Название — как в списке книг (statBook в progress/read.go): из прогресса, иначе
	// из строки хеша с непустым названием и самым поздним last_open.
	rows, err := tx.QueryContext(ctx, `
		SELECT b.id, b.document_id, d.hash,
		       coalesce(c.meta_title, nullif(st.title, '')), coalesce(c.meta_authors, st.authors),
		       c.user_id IS NOT NULL
		FROM stat_books b
		LEFT JOIN documents d ON d.id = b.document_id
		LEFT JOIN progress_current c ON c.user_id = b.user_id AND c.document_id = b.document_id
		LEFT JOIN stat_books st ON st.id = CASE WHEN b.document_id IS NULL THEN b.id ELSE (
			SELECT s.id FROM stat_books s
			WHERE s.user_id = b.user_id AND s.document_id = b.document_id AND s.title <> ''
			ORDER BY s.last_open DESC, s.id DESC LIMIT 1) END
		WHERE b.user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[int64]int64{}
	for rows.Next() {
		var id int64
		var doc sql.NullInt64
		var hash, title, authors sql.NullString
		var ref BookRef
		if err := rows.Scan(&id, &doc, &hash, &title, &authors, &ref.HasProgress); err != nil {
			return nil, err
		}
		key := -id
		if doc.Valid {
			key = doc.Int64
		}
		keys[id] = key
		ref.Title, ref.Authors, ref.Hash = optString(title), optString(authors), optString(hash)
		rep.books[key] = ref
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Условия повторяют вьюху page_stat: JOIN book и деление на total_pages (в SQLite
	// x / 0 = NULL, такая страница не проходит соединение с numbers). Книгу с pages = 0
	// вьюха не отбрасывает: каждая запись даёт страницу 1.
	recs, err := tx.QueryContext(ctx, `
		SELECT p.book_id, p.page, p.start_time, p.duration, p.total_pages, b.pages
		FROM stat_pages p JOIN stat_books b ON b.id = p.book_id
		WHERE p.user_id = ? AND b.pages IS NOT NULL AND p.total_pages <> 0`, userID)
	if err != nil {
		return nil, err
	}
	defer recs.Close()
	for recs.Next() {
		var bookID, page, total, pages int64
		var r record
		if err := recs.Scan(&bookID, &page, &r.start, &r.duration, &total, &pages); err != nil {
			return nil, err
		}
		key, ok := keys[bookID]
		if !ok {
			return nil, fmt.Errorf("statistics: record of unknown book %d", bookID)
		}
		r.key = key
		r.first, r.last = pageRange(page, total, pages)
		rep.records = append(rep.records, r)
	}
	if err := recs.Err(); err != nil {
		return nil, err
	}
	// По ключу, а не по строке книги: записи одного хеша с одним началом идут подряд
	// и попадают в одну сессию, поэтому пара (начало, ключ) у сессий уникальна.
	slices.SortFunc(rep.records, func(a, b record) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.key, b.key))
	})
	return rep, nil
}

// pageRange пересчитывает страницу page из total страниц к текущим pages страницам
// книги по формуле вьюхи page_stat. Деление целочисленное, как в SQLite.
func pageRange(page, total, pages int64) (first, last int64) {
	first = (page-1)*pages/total + 1
	last = max(first, page*pages/total)
	return first, min(last, first+maxPagesPerRecord-1)
}

func optString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// date — календарная дата t в поясе loc как полночь UTC: такие даты сравниваются и
// сдвигаются без учёта переходов на летнее время.
func date(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// bucket — индекс элемента ряда для даты d периода.
func (s *Summary) bucket(d time.Time) int {
	if s.Monthly {
		return (d.Year()-s.From.Year())*12 + int(d.Month()-s.From.Month())
	}
	return int(d.Sub(s.From) / (24 * time.Hour))
}

// sessions строит сессии по упорядоченным записям: новая сессия — при смене книги
// или паузе больше SessionGap после наибольшего конца записей текущей сессии
// (записи двух читалок могут перекрываться).
func (rep *report) sessions() []Session {
	var out []Session
	var pages map[int64]struct{}
	var maxEnd int64
	for _, r := range rep.records {
		n := len(out)
		if n == 0 || out[n-1].key != r.key || r.start-maxEnd > SessionGap {
			if n > 0 {
				out[n-1].Pages = len(pages)
			}
			out = append(out, Session{Book: rep.books[r.key], StartedAt: time.Unix(r.start, 0).UTC(), key: r.key})
			pages, maxEnd = map[int64]struct{}{}, r.start
			n++
		}
		cur := &out[n-1]
		cur.Duration += r.duration
		maxEnd = max(maxEnd, r.start+r.duration)
		cur.EndedAt = time.Unix(maxEnd, 0).UTC()
		for p := r.first; p <= r.last; p++ {
			pages[p] = struct{}{}
		}
	}
	if n := len(out); n > 0 {
		out[n-1].Pages = len(pages)
	}
	return out
}

// dayPage — пересчитанная страница книги, прочитанная в день: за день страница
// считается один раз, как count(*) после GROUP BY id_book, page, dates в
// getDaysFromPeriod KOReader.
type dayPage struct {
	day        time.Time
	book, page int64
}

// daily — время, страницы и сессии периода по дням.
type daily struct {
	duration map[time.Time]int64
	pages    map[time.Time]int
	sessions int
}

func (d *daily) totals() Totals {
	t := Totals{Sessions: d.sessions}
	for _, v := range d.duration {
		t.Duration += v
		if v > 0 {
			t.Days++
		}
	}
	for _, v := range d.pages {
		t.Pages += v
	}
	return t
}

// Summary возвращает итоги периода по сегодня (по now в поясе loc) включительно и
// такого же периода перед ним.
func (s *Store) Summary(ctx context.Context, userID int64, period Period, loc *time.Location, now time.Time) (Summary, error) {
	if !ValidPeriod(period) {
		return Summary{}, errors.New("statistics: unknown period")
	}
	rep, err := s.load(ctx, userID)
	if err != nil {
		return Summary{}, err
	}
	today := date(now, loc)
	sum := Summary{Period: period, To: today, AsOf: rep.asOf, Books: []BookTotal{}}
	var prevFrom time.Time
	switch period {
	case PeriodWeek:
		sum.From = today.AddDate(0, 0, -6)
		prevFrom = sum.From.AddDate(0, 0, -7)
	case PeriodMonth:
		sum.From = today.AddDate(0, 0, -29)
		prevFrom = sum.From.AddDate(0, 0, -30)
	case PeriodYear:
		sum.Monthly = true
		sum.From = time.Date(today.Year(), today.Month()-11, 1, 0, 0, 0, 0, time.UTC)
		prevFrom = sum.From.AddDate(-1, 0, 0)
	}

	if sum.Monthly {
		for m := sum.From; !m.After(today); m = m.AddDate(0, 1, 0) {
			sum.Series = append(sum.Series, Bucket{Date: m})
		}
	} else {
		for d := sum.From; !d.After(today); d = d.AddDate(0, 0, 1) {
			sum.Series = append(sum.Series, Bucket{Date: d})
		}
	}
	if rep.asOf != nil {
		asOf := date(*rep.asOf, loc)
		for i := range sum.Series {
			sum.Series[i].NoData = sum.Series[i].Date.After(asOf)
		}
	}

	cur := daily{duration: map[time.Time]int64{}, pages: map[time.Time]int{}}
	prev := daily{duration: map[time.Time]int64{}, pages: map[time.Time]int{}}
	seen := map[dayPage]struct{}{}
	books := map[int64]*BookTotal{}
	for _, r := range rep.records {
		t := time.Unix(r.start, 0).In(loc)
		d := date(t, loc)
		var acc *daily
		switch {
		case d.Before(prevFrom) || d.After(today):
			continue
		case d.Before(sum.From):
			acc = &prev
		default:
			acc = &cur
		}
		acc.duration[d] += r.duration
		var b *BookTotal
		if acc == &cur {
			sum.Hours[t.Hour()] += r.duration
			sum.Series[sum.bucket(d)].Duration += r.duration
			if b = books[r.key]; b == nil {
				b = &BookTotal{BookRef: rep.books[r.key]}
				books[r.key] = b
			}
			b.Duration += r.duration
		}
		for p := r.first; p <= r.last; p++ {
			k := dayPage{day: d, book: r.key, page: p}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			acc.pages[d]++
			if b != nil {
				b.Pages++
				sum.Series[sum.bucket(d)].Pages++
			}
		}
	}
	for _, ses := range rep.sessions() {
		d := date(ses.StartedAt, loc)
		switch {
		case d.Before(prevFrom) || d.After(today):
		case d.Before(sum.From):
			prev.sessions++
		default:
			cur.sessions++
		}
	}
	sum.Totals, sum.Previous = cur.totals(), prev.totals()

	keys := make([]int64, 0, len(books))
	for k := range books {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b int64) int {
		return cmp.Or(cmp.Compare(books[b].Duration, books[a].Duration), cmp.Compare(a, b))
	})
	for _, k := range keys[:min(len(keys), topBooks)] {
		sum.Books = append(sum.Books, *books[k])
	}
	return sum, nil
}

// Calendar возвращает время чтения по дням с понедельника недели, начавшейся 52
// недели назад от текущей, по сегодня.
func (s *Store) Calendar(ctx context.Context, userID int64, loc *time.Location, now time.Time) (Calendar, error) {
	rep, err := s.load(ctx, userID)
	if err != nil {
		return Calendar{}, err
	}
	today := date(now, loc)
	from := today.AddDate(0, 0, -(int(today.Weekday())+6)%7-52*7)
	cal := Calendar{From: from, To: today, AsOf: rep.asOf, Days: []Day{}}
	byDay := map[time.Time]int64{}
	for _, r := range rep.records {
		d := date(time.Unix(r.start, 0), loc)
		if !d.Before(from) && !d.After(today) {
			byDay[d] += r.duration
		}
	}
	for d, v := range byDay {
		if v > 0 {
			cal.Days = append(cal.Days, Day{Date: d, Duration: v})
		}
	}
	slices.SortFunc(cal.Days, func(a, b Day) int { return a.Date.Compare(b.Date) })
	return cal, nil
}

// Sessions возвращает до limit сессий после курсора (nil — с начала) от последней к
// более ранним и курсор следующей страницы (nil на последней). Сессии строятся заново по всем
// записям, поэтому разбиение на страницы не меняет их границ.
func (s *Store) Sessions(ctx context.Context, userID int64, after *SessionCursor, limit int) ([]Session, *SessionCursor, error) {
	rep, err := s.load(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	all := rep.sessions()
	out := []Session{}
	for i := len(all) - 1; i >= 0; i-- {
		ses := &all[i]
		if !after.before(ses) {
			continue
		}
		if len(out) == limit {
			last := out[len(out)-1]
			return out, &SessionCursor{Start: last.StartedAt.Unix(), Book: last.key}, nil
		}
		out = append(out, *ses)
	}
	return out, nil, nil
}
