// Package store открывает базу SQLite и применяет миграции.
//
// Писатель — один (пул из одного соединения, транзакции BEGIN IMMEDIATE), читатели —
// отдельный пул. WAL и busy_timeout позволяют серверу и CLI одновременно работать
// с одной базой: CLI — отдельный процесс со своими соединениями.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"runtime"

	_ "modernc.org/sqlite" // драйвер "sqlite"
)

// FileName — имя файла базы в каталоге данных.
const FileName = "ksb.sqlite3"

// DB — пара пулов соединений к одной базе.
type DB struct {
	// Write — единственное соединение для записи. Транзакции начинаются с
	// BEGIN IMMEDIATE, чтобы не получать SQLITE_BUSY при повышении блокировки.
	Write *sql.DB
	// Read — пул только для чтения.
	Read *sql.DB
}

// Open открывает базу по пути path и применяет миграции.
func Open(ctx context.Context, path string) (*DB, error) {
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	// journal_mode=WAL сохраняется в файле базы, поэтому первым подключается писатель.
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := Migrate(ctx, w); err != nil {
		w.Close()
		return nil, err
	}

	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(max(4, runtime.GOMAXPROCS(0)))
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, err
	}
	return &DB{Write: w, Read: r}, nil
}

// Close закрывает оба пула.
func (db *DB) Close() error {
	return errors.Join(db.Read.Close(), db.Write.Close())
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}
