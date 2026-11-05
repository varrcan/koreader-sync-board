package progress

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"
)

// FinishedAt — порог «прочитано»: процент, который при показе с одним знаком
// округляется до 100 %. Единица недостижима в режиме прокрутки KOReader, а книга
// с «100 %» на экране не должна навсегда остаться «читаю».
const FinishedAt = 0.9995

// GroupGap — пауза между синхронизациями одной книги с одного устройства,
// начиная с которой в ленте начинается новая запись.
const GroupGap = 30 * time.Minute

// Filter — отбор книг в списке.
type Filter string

const (
	FilterAll      Filter = ""
	FilterReading  Filter = "reading"
	FilterFinished Filter = "finished"
	FilterUnknown  Filter = "unknown"
)

// Book — книга пользователя: метаданные из его синхронизаций и последнее событие.
type Book struct {
	Hash                     string
	Title, Authors, Filename *string
	Percentage               float64
	Finished                 bool
	UpdatedAt                time.Time
	// DeviceName — имя учётных данных устройства; nil, если они удалены.
	DeviceName     *string
	ReportedDevice string
}

// Counts — число книг пользователя по фильтрам.
type Counts struct{ All, Reading, Finished, Unknown int }

// BookPage — страница списка книг. Next — курсор следующей страницы, 0 на последней.
type BookPage struct {
	Items  []Book
	Next   int64
	Counts Counts
}

// DeviceState — последнее состояние книги на одном устройстве (учётных данных).
type DeviceState struct {
	DeviceName     *string
	ReportedDevice string
	Percentage     float64
	UpdatedAt      time.Time
}

// Group — запись ленты: серия синхронизаций одного документа с одних учётных
// данных без пауз ≥ GroupGap.
type Group struct {
	Hash                         string
	Title, Authors, Filename     *string
	DeviceName                   *string
	ReportedDevice               string
	FromPercentage, ToPercentage float64
	StartedAt, EndedAt           time.Time
	Syncs                        int
}

// ActivityPage — страница ленты. Next — курсор следующей страницы, 0 на последней.
type ActivityPage struct {
	Items []Group
	Next  int64
}

const unknownCond = `c.meta_title IS NULL AND c.meta_filename IS NULL`

var filterCond = map[Filter]string{
	FilterAll:      ``,
	FilterReading:  ` AND e.percentage < ?2`,
	FilterFinished: ` AND e.percentage >= ?2`,
	FilterUnknown:  ` AND ` + unknownCond,
}

// ValidFilter сообщает, известен ли фильтр.
func ValidFilter(f Filter) bool {
	_, ok := filterCond[f]
	return ok
}

const bookColumns = `d.hash, c.meta_title, c.meta_authors, c.meta_filename,
	e.percentage, e.server_ts, cr.name, e.device, c.event_id
	FROM progress_current c
	JOIN documents d ON d.id = c.document_id
	JOIN progress_events e ON e.id = c.event_id
	LEFT JOIN device_credentials cr ON cr.id = e.credential_id`

// Books возвращает страницу книг пользователя от последней синхронизации к более
// ранним и счётчики по всем его книгам. before — курсор (0 — с начала).
//
// Ключ и курсор — event_id последнего события: его выдаёт единственный писатель,
// он уникален и монотонен. Книга, синхронизированная во время листания, уходит
// выше курсора и повторно не приходит.
func (s *Store) Books(ctx context.Context, userID int64, f Filter, before int64, limit int) (BookPage, error) {
	cond, ok := filterCond[f]
	if !ok {
		return BookPage{}, errors.New("progress: unknown filter")
	}
	if before <= 0 {
		before = math.MaxInt64
	}
	// Счётчики и страница — из одного снимка: пул чтения без транзакции может
	// выполнить запросы в разных соединениях между двумя записями.
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BookPage{}, err
	}
	defer tx.Rollback()

	var page BookPage
	c := &page.Counts
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*),
		       coalesce(sum(e.percentage < ?2), 0),
		       coalesce(sum(e.percentage >= ?2), 0),
		       coalesce(sum(`+unknownCond+`), 0)
		FROM progress_current c
		JOIN progress_events e ON e.id = c.event_id
		WHERE c.user_id = ?1`, userID, FinishedAt).Scan(&c.All, &c.Reading, &c.Finished, &c.Unknown); err != nil {
		return BookPage{}, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT `+bookColumns+`
		WHERE c.user_id = ?1 AND c.event_id < ?3`+cond+`
		ORDER BY c.event_id DESC
		LIMIT ?4`, userID, FinishedAt, before, limit+1)
	if err != nil {
		return BookPage{}, err
	}
	defer rows.Close()
	page.Items = []Book{}
	var last int64
	for rows.Next() {
		if len(page.Items) == limit {
			page.Next = last
			break
		}
		b, id, err := scanBook(rows)
		if err != nil {
			return BookPage{}, err
		}
		page.Items = append(page.Items, b)
		last = id
	}
	return page, rows.Err()
}

// Book возвращает книгу пользователя и последнее состояние на каждом устройстве,
// от последней синхронизации к более ранним. Чужая и несуществующая книга —
// ErrNoProgress.
func (s *Store) Book(ctx context.Context, userID int64, hash string) (Book, []DeviceState, error) {
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Book{}, nil, err
	}
	defer tx.Rollback()

	b, _, err := scanBook(tx.QueryRowContext(ctx, `SELECT `+bookColumns+`
		WHERE c.user_id = ? AND d.hash = ?`, userID, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return Book{}, nil, ErrNoProgress
	}
	if err != nil {
		return Book{}, nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT cr.name, e.device, e.percentage, e.server_ts
		FROM progress_events e
		LEFT JOIN device_credentials cr ON cr.id = e.credential_id
		WHERE e.id IN (
			SELECT max(e2.id) FROM progress_events e2
			JOIN documents d ON d.id = e2.document_id
			WHERE e2.user_id = ? AND d.hash = ?
			GROUP BY e2.credential_id)
		ORDER BY e.id DESC`, userID, hash)
	if err != nil {
		return Book{}, nil, err
	}
	defer rows.Close()
	devices := []DeviceState{}
	for rows.Next() {
		var d DeviceState
		var name sql.NullString
		var ts int64
		if err := rows.Scan(&name, &d.ReportedDevice, &d.Percentage, &ts); err != nil {
			return Book{}, nil, err
		}
		d.DeviceName = nullable(name)
		d.UpdatedAt = time.Unix(ts, 0).UTC()
		devices = append(devices, d)
	}
	return b, devices, rows.Err()
}

// Activity возвращает ленту пользователя, свёрнутую в группы, от последней
// синхронизации к более ранним; hash ограничивает ленту одной книгой (книги нет
// у пользователя — ErrNoProgress). before — курсор (0 — с начала).
//
// Группы считаются по всему журналу, а курсор (last_id группы) применяется к уже
// готовым группам: ограничение входа по id разрезало бы группу на границе
// страницы. Серия считается внутри пары (документ, учётные данные), поэтому
// чтение двух книг или на двух устройствах вперемешку группы не рвёт.
func (s *Store) Activity(ctx context.Context, userID int64, hash string, before int64, limit int) (ActivityPage, error) {
	if before <= 0 {
		before = math.MaxInt64
	}
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ActivityPage{}, err
	}
	defer tx.Rollback()

	var docID any
	if hash != "" {
		var id int64
		err := tx.QueryRowContext(ctx, `
			SELECT c.document_id FROM progress_current c
			JOIN documents d ON d.id = c.document_id
			WHERE c.user_id = ? AND d.hash = ?`, userID, hash).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ActivityPage{}, ErrNoProgress
		}
		if err != nil {
			return ActivityPage{}, err
		}
		docID = id
	}

	// lag первого события пары — NULL, сравнение даёт NULL, и CASE открывает группу.
	rows, err := tx.QueryContext(ctx, `
		WITH ev AS (
			SELECT e.id, e.document_id, e.credential_id,
			       CASE WHEN e.server_ts - lag(e.server_ts) OVER w < ?3 THEN 0 ELSE 1 END AS head
			FROM progress_events e
			WHERE e.user_id = ?1 AND (?2 IS NULL OR e.document_id = ?2)
			WINDOW w AS (PARTITION BY e.document_id, e.credential_id ORDER BY e.id)
		), grp AS (
			SELECT id, document_id, credential_id,
			       sum(head) OVER (PARTITION BY document_id, credential_id ORDER BY id) AS g
			FROM ev
		), groups AS (
			SELECT document_id, min(id) AS first_id, max(id) AS last_id, count(*) AS syncs
			FROM grp GROUP BY document_id, credential_id, g
		)
		SELECT d.hash, c.meta_title, c.meta_authors, c.meta_filename,
		       cr.name, l.device, f.percentage, l.percentage, f.server_ts, l.server_ts,
		       groups.syncs, groups.last_id
		FROM groups
		JOIN progress_events f ON f.id = groups.first_id
		JOIN progress_events l ON l.id = groups.last_id
		JOIN documents d ON d.id = groups.document_id
		LEFT JOIN progress_current c ON c.user_id = ?1 AND c.document_id = groups.document_id
		LEFT JOIN device_credentials cr ON cr.id = l.credential_id
		WHERE groups.last_id < ?4
		ORDER BY groups.last_id DESC
		LIMIT ?5`, userID, docID, int64(GroupGap/time.Second), before, limit+1)
	if err != nil {
		return ActivityPage{}, err
	}
	defer rows.Close()
	page := ActivityPage{Items: []Group{}}
	var last int64
	for rows.Next() {
		if len(page.Items) == limit {
			page.Next = last
			break
		}
		var g Group
		var title, authors, filename, name sql.NullString
		var from, to int64
		if err := rows.Scan(&g.Hash, &title, &authors, &filename, &name, &g.ReportedDevice,
			&g.FromPercentage, &g.ToPercentage, &from, &to, &g.Syncs, &last); err != nil {
			return ActivityPage{}, err
		}
		g.Title, g.Authors, g.Filename = nullable(title), nullable(authors), nullable(filename)
		g.DeviceName = nullable(name)
		g.StartedAt, g.EndedAt = time.Unix(from, 0).UTC(), time.Unix(to, 0).UTC()
		page.Items = append(page.Items, g)
	}
	return page, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanBook(r scanner) (Book, int64, error) {
	var b Book
	var title, authors, filename, name sql.NullString
	var ts, eventID int64
	if err := r.Scan(&b.Hash, &title, &authors, &filename, &b.Percentage, &ts, &name,
		&b.ReportedDevice, &eventID); err != nil {
		return Book{}, 0, err
	}
	b.Title, b.Authors, b.Filename = nullable(title), nullable(authors), nullable(filename)
	b.DeviceName = nullable(name)
	b.Finished = b.Percentage >= FinishedAt
	b.UpdatedAt = time.Unix(ts, 0).UTC()
	return b, eventID, nil
}

func nullable(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
