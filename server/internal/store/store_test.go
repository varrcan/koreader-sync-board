package store

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestOpenCreatesSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, table := range []string{"users", "device_credentials", "documents", "progress_events", "progress_current", "sessions"} {
		var n int
		if err := db.Read.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	var mode string
	if err := db.Read.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %s, want wal", mode)
	}
	var fk int
	if err := db.Write.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Error("foreign_keys disabled")
	}
	if _, err := db.Read.ExecContext(ctx, `INSERT INTO documents (hash, first_seen_at) VALUES ('x', 1)`); err == nil {
		t.Error("read pool accepted a write")
	}
}

func TestMigrateTwice(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	for range 2 {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.Read.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(ms) {
		t.Fatalf("schema_migrations rows = %d, want %d", n, len(ms))
	}
	if err := Migrate(ctx, db.Write); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateAppliesNewOnly(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fsys := fstest.MapFS{
		"migrations/0001_init.sql":  {Data: []byte(`SELECT RAISE(ABORT, 'must not run again')`)},
		"migrations/9999_extra.sql": {Data: []byte(`CREATE TABLE extra (id INTEGER PRIMARY KEY)`)},
	}
	if err := migrate(ctx, db.Write, fsys); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write.ExecContext(ctx, `INSERT INTO extra (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
}

func TestEventsAppendOnly(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec := func(q string) {
		t.Helper()
		if _, err := db.Write.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users (id, username, role, created_at) VALUES (1, 'u', 'user', 0)`)
	mustExec(`INSERT INTO documents (id, hash, first_seen_at) VALUES (1, 'h', 0)`)
	mustExec(`INSERT INTO progress_events (user_id, document_id, percentage, progress, device, server_ts) VALUES (1, 1, 0.1, 'p', 'd', 0)`)
	if _, err := db.Write.ExecContext(ctx, `UPDATE progress_events SET percentage = 0.2`); err == nil {
		t.Fatal("update of progress_events succeeded")
	}
}

// TestTwoConnections: сервер и CLI держат разные пулы к одному файлу.
func TestTwoConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.Write.ExecContext(ctx, `INSERT INTO documents (hash, first_seen_at) VALUES ('h', 1)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := a.Read.QueryRowContext(ctx, `SELECT count(*) FROM documents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("other connection sees %d documents", n)
	}
}
