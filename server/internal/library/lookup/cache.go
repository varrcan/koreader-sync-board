package lookup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

const cacheTTL = 7 * 24 * time.Hour

// cache хранит нормализованных кандидатов: одна запись на запрос вместо цепочки
// ответов (поиск и детали каждого результата).
type cache struct {
	db  *store.DB
	now func() time.Time
}

func newCache(db *store.DB) *cache { return &cache{db: db, now: time.Now} }

func (c *cache) get(ctx context.Context, provider, key string) ([]Candidate, bool, error) {
	var data string
	err := c.db.Read.QueryRowContext(ctx, `SELECT candidates FROM library_metadata_cache
		WHERE provider = ? AND query = ? AND expires_at > ?`, provider, key, c.now().Unix()).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var items []Candidate
	if err := json.Unmarshal([]byte(data), &items); err != nil {
		return nil, false, err
	}
	return items, true, nil
}

// put записывает результат, в том числе пустой, и убирает просроченное.
func (c *cache) put(ctx context.Context, provider, key string, items []Candidate) error {
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	tx, err := c.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := c.now()
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_metadata_cache WHERE expires_at <= ?`, now.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO library_metadata_cache (provider, query, candidates, expires_at)
		VALUES (?, ?, ?, ?)`, provider, key, string(data), now.Add(cacheTTL).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
