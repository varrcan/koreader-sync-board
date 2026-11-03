// Package progress хранит прогресс чтения: журнал событий только на добавление
// и текущее состояние на пару (пользователь, документ).
package progress

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

// Metadata — необязательные поля metadata из PUT /syncs/progress. Пустая строка
// означает «не прислано» и не затирает сохранённое значение.
type Metadata struct {
	Filename string
	Title    string
	Authors  string
}

func (m Metadata) empty() bool { return m.Filename == "" && m.Title == "" && m.Authors == "" }

// Update — одно принятое обновление прогресса.
type Update struct {
	Document   string
	Percentage float64
	Progress   string
	Device     string
	DeviceID   *string
	// MetaJSON — присланный объект metadata как есть (nil, если это не объект).
	MetaJSON []byte
	Meta     Metadata
	// ReceivedAt — время получения запроса; клиентское время не используется.
	ReceivedAt time.Time
}

// Current — последнее состояние пары (пользователь, документ).
type Current struct {
	Percentage float64
	Progress   string
	Device     string
	DeviceID   *string
	Timestamp  int64
	Meta       Metadata
}

// Store читает и пишет прогресс.
type Store struct {
	db *store.DB
}

// New создаёт Store.
func New(db *store.DB) *Store { return &Store{db: db} }

// Save в одной транзакции повторно проверяет устройство, регистрирует документ,
// добавляет событие и обновляет текущее состояние. Если устройство отозвано,
// пользователь отключён или ключ сменился после проверки, возвращается
// accounts.ErrUnauthorized и ничего не пишется.
func (s *Store) Save(ctx context.Context, p accounts.Principal, u Update) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := accounts.CheckTx(ctx, tx, p); err != nil {
		return err
	}
	ts := u.ReceivedAt.Unix()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO documents (hash, first_seen_at) VALUES (?, ?) ON CONFLICT (hash) DO NOTHING`,
		u.Document, ts); err != nil {
		return err
	}
	var docID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM documents WHERE hash = ?`, u.Document).Scan(&docID); err != nil {
		return err
	}

	var metaJSON any
	if u.MetaJSON != nil {
		metaJSON = string(u.MetaJSON)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO progress_events
			(user_id, document_id, credential_id, percentage, progress, device, device_id, meta_json, server_ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.UserID, docID, p.CredentialID, u.Percentage, u.Progress, u.Device, u.DeviceID, metaJSON, ts)
	if err != nil {
		return err
	}
	eventID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	var metaTS any
	if !u.Meta.empty() {
		metaTS = ts
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO progress_current
			(user_id, document_id, event_id, meta_filename, meta_title, meta_authors, meta_updated_at)
		VALUES (?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?)
		ON CONFLICT (user_id, document_id) DO UPDATE SET
			event_id        = excluded.event_id,
			meta_filename   = coalesce(excluded.meta_filename, meta_filename),
			meta_title      = coalesce(excluded.meta_title, meta_title),
			meta_authors    = coalesce(excluded.meta_authors, meta_authors),
			meta_updated_at = coalesce(excluded.meta_updated_at, meta_updated_at)`,
		p.UserID, docID, eventID, u.Meta.Filename, u.Meta.Title, u.Meta.Authors, metaTS); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrNoProgress — у пользователя нет записей по документу.
var ErrNoProgress = errors.New("no progress")

// Get возвращает текущее состояние пользователя по документу.
//
// Эталон хранит прогресс в хеше Redis и пишет device_id, только если он прислан,
// поэтому при обновлении без device_id остаётся прежнее значение. Повторяем это:
// берём device_id из последнего события пары, где он был.
func (s *Store) Get(ctx context.Context, userID int64, document string) (Current, error) {
	var c Current
	var deviceID, filename, title, authors sql.NullString
	err := s.db.Read.QueryRowContext(ctx, `
		SELECT e.percentage, e.progress, e.device,
		       coalesce(e.device_id, (
		           SELECT e2.device_id FROM progress_events e2
		           WHERE e2.user_id = c.user_id AND e2.document_id = c.document_id
		             AND e2.device_id IS NOT NULL
		           ORDER BY e2.id DESC LIMIT 1)),
		       e.server_ts, c.meta_filename, c.meta_title, c.meta_authors
		FROM progress_current c
		JOIN documents d ON d.id = c.document_id
		JOIN progress_events e ON e.id = c.event_id
		WHERE c.user_id = ? AND d.hash = ?`,
		userID, document).Scan(&c.Percentage, &c.Progress, &c.Device, &deviceID, &c.Timestamp,
		&filename, &title, &authors)
	if errors.Is(err, sql.ErrNoRows) {
		return Current{}, ErrNoProgress
	}
	if err != nil {
		return Current{}, err
	}
	if deviceID.Valid {
		c.DeviceID = &deviceID.String
	}
	c.Meta = Metadata{Filename: filename.String, Title: title.String, Authors: authors.String}
	return c, nil
}
