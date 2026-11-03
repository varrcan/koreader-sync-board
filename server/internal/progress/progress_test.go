package progress

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

func TestSaveIsAtomic(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	acc := accounts.New(db, accounts.WithArgon2Params(accounts.TestArgon2Params))
	if _, err := acc.CreateUser(ctx, "alice", accounts.RoleUser); err != nil {
		t.Fatal(err)
	}
	pass, err := acc.AddDevice(ctx, "alice", "Kindle")
	if err != nil {
		t.Fatal(err)
	}
	p, err := acc.Authenticate(ctx, "alice", accounts.KeyFromPassword(pass))
	if err != nil {
		t.Fatal(err)
	}
	s := New(db)
	u := Update{Document: "abc", Percentage: 0.1, Progress: "p1", Device: "d", ReceivedAt: time.Unix(100, 0),
		Meta: Metadata{Title: "T1"}}
	if err := s.Save(ctx, p, u); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Write.Exec(`CREATE TEMP TRIGGER fail BEFORE INSERT ON progress_events BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	u2 := Update{Document: "abc", Percentage: 0.9, Progress: "p2", Device: "d", ReceivedAt: time.Unix(200, 0),
		Meta: Metadata{Title: "T2"}}
	if err := s.Save(ctx, p, u2); err == nil {
		t.Fatal("save succeeded despite failing insert")
	}
	u3 := u2
	u3.Document = "newdoc"
	if err := s.Save(ctx, p, u3); err == nil {
		t.Fatal("save succeeded despite failing insert")
	}

	cur, err := s.Get(ctx, p.UserID, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Progress != "p1" || cur.Meta.Title != "T1" || cur.Timestamp != 100 {
		t.Fatalf("current changed: %+v", cur)
	}
	var docs int
	db.Read.QueryRow(`SELECT count(*) FROM documents`).Scan(&docs)
	if docs != 1 {
		t.Fatalf("documents = %d: registry written outside the transaction", docs)
	}
}
