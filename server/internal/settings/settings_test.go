package settings

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

func open(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMetadataSuggest(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), store.FileName)
	db := open(t, path)
	s := New(db)
	if on, err := s.MetadataSuggest(ctx); err != nil || on {
		t.Fatalf("default: %v, %v", on, err)
	}
	changes := 0
	s.OnChange = func() { changes++ }
	if err := s.SetMetadataSuggest(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, err := s.MetadataSuggest(ctx); err != nil || !on {
		t.Fatalf("after set: %v, %v", on, err)
	}
	if changes != 1 {
		t.Fatalf("OnChange called %d times", changes)
	}
	db.Close()

	db = open(t, path)
	defer db.Close()
	if on, err := New(db).MetadataSuggest(ctx); err != nil || !on {
		t.Fatalf("after reopen: %v, %v", on, err)
	}
	s = New(db)
	if err := s.SetMetadataSuggest(ctx, false); err != nil {
		t.Fatal(err)
	}
	if on, err := s.MetadataSuggest(ctx); err != nil || on {
		t.Fatalf("after disable: %v, %v", on, err)
	}
}
