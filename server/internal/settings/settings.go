// Package settings хранит настройки сервера, которые администратор меняет в панели.
// Значение по умолчанию задаёт код: ключа нет в БД, пока настройку не сохранили.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

const keyMetadataSuggest = "library_metadata_suggest"

// Store — таблица settings.
type Store struct {
	db *store.DB
	// OnChange вызывается после сохранения настройки: фоновая работа просыпается
	// сразу, а не по таймеру. Задаётся до начала обслуживания запросов.
	OnChange func()
}

// New создаёт Store.
func New(db *store.DB) *Store { return &Store{db: db} }

// MetadataSuggest — включён ли фоновый поиск метаданных; по умолчанию выключен.
func (s *Store) MetadataSuggest(ctx context.Context) (bool, error) {
	v, ok, err := s.get(ctx, keyMetadataSuggest)
	if err != nil || !ok {
		return false, err
	}
	return strconv.ParseBool(v)
}

// SetMetadataSuggest сохраняет настройку фонового поиска.
func (s *Store) SetMetadataSuggest(ctx context.Context, on bool) error {
	return s.set(ctx, keyMetadataSuggest, strconv.FormatBool(on))
}

func (s *Store) get(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.Read.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) set(ctx context.Context, key, value string) error {
	if _, err := s.db.Write.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return err
	}
	if s.OnChange != nil {
		s.OnChange()
	}
	return nil
}
