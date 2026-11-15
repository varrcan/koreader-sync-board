package store

import (
	"context"
	"fmt"
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

	for _, table := range []string{"users", "device_credentials", "documents", "progress_events", "progress_current", "sessions", "webdav_files", "stat_books", "stat_pages"} {
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

// TestDeleteUserCascadesStatistics: удаление пользователя убирает его файлы WebDAV
// и статистику, а общий реестр документов не трогает.
func TestDeleteUserCascadesStatistics(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO users (id, username, role, created_at) VALUES (1, 'alice', 'user', 0), (2, 'bob', 'user', 0)`,
		`INSERT INTO device_credentials (id, user_id, name, key_hash, created_at) VALUES (1, 1, 'Kobo', 'h', 0)`,
		`INSERT INTO documents (id, hash, first_seen_at) VALUES (1, 'h', 0)`,
		`INSERT INTO webdav_files (user_id, name, content, etag, size, modified_at, credential_id, source)
		 VALUES (1, 'statistics.sqlite3', x'00', '"e"', 1, 0, 1, 'webdav'), (2, 'statistics.sqlite3', x'00', '"e"', 1, 0, NULL, 'import')`,
		`INSERT INTO stat_books (id, user_id, document_id, title) VALUES (1, 1, 1, 'A'), (2, 2, 1, 'B')`,
		`INSERT INTO stat_pages (book_id, user_id, page, start_time, duration, total_pages) VALUES (1, 1, 1, 1, 1, 1), (2, 2, 1, 1, 1, 1)`,
		`DELETE FROM users WHERE id = 1`,
	} {
		if _, err := db.Write.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for table, want := range map[string]int{"webdav_files": 1, "stat_books": 1, "stat_pages": 1, "documents": 1} {
		var n int
		if err := db.Read.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s: %d rows, want %d", table, n, want)
		}
	}
}

// TestMigrateLibraryOverExistingData: 0007 применяется к базе с данными 0006 и
// не трогает их.
func TestMigrateLibraryOverExistingData(t *testing.T) {
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
	for _, table := range []string{"library_file_hashes", "library_files", "library_book_keys", "library_books", "library_roots"} {
		mustExec(`DROP TABLE ` + table)
	}
	mustExec(`DELETE FROM schema_migrations WHERE version = 7`)
	mustExec(`INSERT INTO users (id, username, role, created_at, oidc_issuer, oidc_subject) VALUES (1, 'u', 'user', 0, 'i', 's')`)
	mustExec(`INSERT INTO documents (id, hash, first_seen_at) VALUES (1, 'h', 0)`)
	mustExec(`INSERT INTO progress_events (user_id, document_id, percentage, progress, device, server_ts) VALUES (1, 1, 0.1, 'p', 'd', 0)`)

	if err := Migrate(ctx, db.Write); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Read.QueryRowContext(ctx, `SELECT count(*) FROM progress_events JOIN users ON users.id = user_id WHERE oidc_subject = 's'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("existing data: %d, %v", n, err)
	}
	mustExec(`INSERT INTO library_roots (id, name, path, created_at) VALUES (1, 'books', '/books', 0)`)
	mustExec(`INSERT INTO library_books (id, created_at) VALUES (1, 0)`)
	mustExec(`INSERT INTO library_book_keys (key, book_id) VALUES ('k', 1)`)
	mustExec(`INSERT INTO library_files (id, root_id, path, size, mtime_ns, format, partial_md5, book_id, indexed_at) VALUES (1, 1, 'a.epub', 1, 1, 'epub', 'h', 1, 0)`)
	mustExec(`INSERT INTO library_file_hashes (hash, file_id) VALUES ('h', 1)`)
	// Корень уносит файлы и их хеши каскадом; книгу без файлов убирает сканер.
	mustExec(`DELETE FROM library_roots`)
	if err := db.Read.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM library_files) + (SELECT count(*) FROM library_file_hashes)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("after root delete: %d, %v", n, err)
	}
	mustExec(`DELETE FROM library_books`)
	if err := db.Read.QueryRowContext(ctx, `SELECT count(*) FROM library_book_keys`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("keys after book delete: %d, %v", n, err)
	}
}

// TestMigrateManagedRootKinds: 0012 сохраняет корень конвертаций и допускает по
// одному служебному корню каждого вида.
func TestMigrateManagedRootKinds(t *testing.T) {
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
	mustExec(`DROP INDEX library_roots_managed`)
	mustExec(`CREATE UNIQUE INDEX library_roots_managed ON library_roots (managed) WHERE managed = 1`)
	mustExec(`DELETE FROM schema_migrations WHERE version = 12`)
	mustExec(`INSERT INTO library_roots (name, path, created_at, managed) VALUES ('converted', '/data/converted', 0, 1)`)

	if err := Migrate(ctx, db.Write); err != nil {
		t.Fatal(err)
	}
	mustExec(`INSERT INTO library_roots (name, path, created_at, managed) VALUES ('uploads', '/data/uploads', 0, 2)`)
	mustExec(`INSERT INTO library_roots (name, path, created_at) VALUES ('a', '/a', 0), ('b', '/b', 0)`)
	for _, kind := range []int{1, 2} {
		if _, err := db.Write.ExecContext(ctx, `INSERT INTO library_roots (name, path, created_at, managed) VALUES ('x', ?, 0, ?)`,
			fmt.Sprintf("/x%d", kind), kind); err == nil {
			t.Errorf("second managed root of kind %d accepted", kind)
		}
	}
	var n int
	if err := db.Read.QueryRowContext(ctx, `SELECT count(*) FROM library_roots`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("roots: %d, %v", n, err)
	}
}
