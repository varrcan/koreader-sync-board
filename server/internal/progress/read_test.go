package progress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

type readEnv struct {
	t   *testing.T
	ctx context.Context
	acc *accounts.Service
	s   *Store
	// now — время следующего события; at сдвигает его.
	now time.Time
}

func newReadEnv(t *testing.T) *readEnv {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &readEnv{t: t, ctx: ctx, acc: accounts.New(db, accounts.WithArgon2Params(accounts.TestArgon2Params)),
		s: New(db), now: time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)}
}

func (e *readEnv) user(name string) int64 {
	e.t.Helper()
	u, err := e.acc.CreateUser(e.ctx, name, accounts.RoleUser)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

func (e *readEnv) device(user, name string) accounts.Principal {
	e.t.Helper()
	_, pass, err := e.acc.AddDevice(e.ctx, user, name)
	if err != nil {
		e.t.Fatal(err)
	}
	p, err := e.acc.Authenticate(e.ctx, user, accounts.KeyFromPassword(pass), netip.Addr{})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// sync сохраняет событие через d после текущего времени и сдвигает его.
func (e *readEnv) sync(p accounts.Principal, doc string, pct float64, after time.Duration, meta Metadata) {
	e.t.Helper()
	e.now = e.now.Add(after)
	if err := e.s.Save(e.ctx, p, Update{Document: doc, Percentage: pct, Progress: "x", Device: "reader-" + p.Username,
		Meta: meta, ReceivedAt: e.now}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *readEnv) books(userID int64, f Filter, before int64, limit int) BookPage {
	e.t.Helper()
	p, err := e.s.Books(e.ctx, userID, f, before, limit)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func hashes(bs []Book) string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Hash)
	}
	return strings.Join(out, ",")
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestBooksOrderAndPages(t *testing.T) {
	e := newReadEnv(t)
	alice := e.user("alice")
	pb := e.device("alice", "pocketbook")
	e.sync(pb, "A", 0.1, time.Minute, Metadata{})
	e.sync(pb, "B", 0.2, time.Minute, Metadata{})
	e.sync(pb, "C", 0.3, time.Minute, Metadata{})
	e.sync(pb, "A", 0.15, time.Minute, Metadata{})

	if got := hashes(e.books(alice, FilterAll, 0, 50).Items); got != "A,C,B" {
		t.Fatalf("order %s", got)
	}

	p1 := e.books(alice, FilterAll, 0, 2)
	if hashes(p1.Items) != "A,C" || p1.Next == 0 {
		t.Fatalf("page 1 %s next %d", hashes(p1.Items), p1.Next)
	}
	// Книга со следующей страницы синхронизировалась во время листания.
	e.sync(pb, "B", 0.25, time.Minute, Metadata{})
	p2 := e.books(alice, FilterAll, p1.Next, 2)
	if len(p2.Items) != 0 || p2.Next != 0 {
		t.Fatalf("page 2 %s next %d: B moved above the cursor", hashes(p2.Items), p2.Next)
	}
	if got := hashes(e.books(alice, FilterAll, 0, 50).Items); got != "B,A,C" {
		t.Fatalf("after sync %s", got)
	}

	p1 = e.books(alice, FilterAll, 0, 2)
	p2 = e.books(alice, FilterAll, p1.Next, 2)
	if hashes(p2.Items) != "C" || p2.Next != 0 || p2.Counts.All != 3 {
		t.Fatalf("last page %s next %d counts %+v", hashes(p2.Items), p2.Next, p2.Counts)
	}
	if exact := e.books(alice, FilterAll, 0, 3); exact.Next != 0 {
		t.Fatalf("next on an exactly full page: %d", exact.Next)
	}
}

func TestBooksFiltersAndCounts(t *testing.T) {
	e := newReadEnv(t)
	alice := e.user("alice")
	pb := e.device("alice", "pocketbook")
	e.sync(pb, "read1", 0.4, time.Minute, Metadata{Title: "Read 1"})
	e.sync(pb, "unknown", 0.2, time.Minute, Metadata{})
	e.sync(pb, "done", 1, time.Minute, Metadata{Filename: "done.epub"})

	want := Counts{All: 3, Reading: 2, Finished: 1, Unknown: 1}
	for f, hs := range map[Filter]string{
		FilterAll: "done,unknown,read1", FilterReading: "unknown,read1",
		FilterFinished: "done", FilterUnknown: "unknown",
	} {
		p := e.books(alice, f, 0, 1)
		if got := hashes(e.books(alice, f, 0, 50).Items); got != hs {
			t.Errorf("filter %q: %s, want %s", f, got, hs)
		}
		if p.Counts != want {
			t.Errorf("filter %q counts %+v, want %+v", f, p.Counts, want)
		}
	}
	if _, err := e.s.Books(e.ctx, alice, "deleted", 0, 10); err == nil {
		t.Fatal("unknown filter accepted")
	}
	if p := e.books(e.user("empty"), FilterAll, 0, 10); len(p.Items) != 0 || p.Counts != (Counts{}) {
		t.Fatalf("empty user %+v", p)
	}
}

func TestFinishedThresholdAndUnknown(t *testing.T) {
	e := newReadEnv(t)
	alice := e.user("alice")
	pb := e.device("alice", "pocketbook")
	cases := []struct {
		pct      float64
		meta     Metadata
		finished bool
		unknown  bool
	}{
		{0.9995, Metadata{}, true, true},
		{0.9996, Metadata{Title: "T"}, true, false},
		{0.994, Metadata{Filename: "only.fb2"}, false, false},
		{0.99949, Metadata{Authors: "only author"}, false, true},
	}
	for i, c := range cases {
		e.sync(pb, fmt.Sprint("doc", i), c.pct, time.Minute, c.meta)
	}
	finished := hashes(e.books(alice, FilterFinished, 0, 50).Items)
	unknown := hashes(e.books(alice, FilterUnknown, 0, 50).Items)
	for i, c := range cases {
		h := fmt.Sprint("doc", i)
		b, _, err := e.s.Book(e.ctx, alice, h)
		if err != nil {
			t.Fatal(err)
		}
		if b.Finished != c.finished || strings.Contains(","+finished+",", ","+h+",") != c.finished {
			t.Errorf("%v: finished %v, in filter %q", c.pct, b.Finished, finished)
		}
		if strings.Contains(","+unknown+",", ","+h+",") != c.unknown {
			t.Errorf("%+v: unknown filter %q", c.meta, unknown)
		}
	}
}

func TestBooksDevicesAndUsers(t *testing.T) {
	e := newReadEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	pb := e.device("alice", "pocketbook")
	bb := e.device("bob", "kobo")
	e.sync(pb, "shared", 0.3, time.Minute, Metadata{Title: "Alice title", Authors: "A"})
	e.sync(bb, "shared", 0.7, time.Minute, Metadata{Title: "Bob title"})
	e.sync(bb, "bobonly", 0.1, time.Minute, Metadata{})

	if err := e.acc.RevokeDevice(e.ctx, "alice", "pocketbook"); err != nil {
		t.Fatal(err)
	}
	a := e.books(alice, FilterAll, 0, 50)
	if len(a.Items) != 1 || a.Counts.All != 1 {
		t.Fatalf("alice sees %s", hashes(a.Items))
	}
	b := a.Items[0]
	if str(b.Title) != "Alice title" || str(b.Authors) != "A" || b.Filename != nil || b.Percentage != 0.3 ||
		str(b.DeviceName) != "pocketbook" || b.ReportedDevice != "reader-alice" || b.UpdatedAt.Location() != time.UTC {
		t.Fatalf("alice book %+v", b)
	}
	bobs := e.books(bob, FilterAll, 0, 50)
	if hashes(bobs.Items) != "bobonly,shared" || str(bobs.Items[1].Title) != "Bob title" || bobs.Items[1].Percentage != 0.7 {
		t.Fatalf("bob books %+v", bobs.Items)
	}
	if _, _, err := e.s.Book(e.ctx, alice, "bobonly"); !errors.Is(err, ErrNoProgress) {
		t.Fatalf("foreign book: %v", err)
	}
}

func TestBookDevices(t *testing.T) {
	e := newReadEnv(t)
	alice := e.user("alice")
	pb := e.device("alice", "pocketbook")
	x4 := e.device("alice", "xteink")
	e.sync(pb, "H", 0.2, time.Minute, Metadata{Title: "T"})
	e.sync(pb, "H", 0.4, time.Minute, Metadata{})
	e.sync(x4, "H", 0.45, time.Minute, Metadata{})
	e.sync(pb, "other", 0.9, time.Minute, Metadata{})

	b, ds, err := e.s.Book(e.ctx, alice, "H")
	if err != nil {
		t.Fatal(err)
	}
	if b.Percentage != 0.45 || str(b.DeviceName) != "xteink" || str(b.Title) != "T" {
		t.Fatalf("book %+v", b)
	}
	if len(ds) != 2 || str(ds[0].DeviceName) != "xteink" || ds[0].Percentage != 0.45 ||
		str(ds[1].DeviceName) != "pocketbook" || ds[1].Percentage != 0.4 || !ds[0].UpdatedAt.After(ds[1].UpdatedAt) {
		t.Fatalf("devices %+v", ds)
	}

	bob := e.user("bob")
	for _, c := range []struct {
		user int64
		hash string
	}{{bob, "H"}, {alice, "missing"}} {
		if _, _, err := e.s.Book(e.ctx, c.user, c.hash); !errors.Is(err, ErrNoProgress) {
			t.Fatalf("%d/%s: %v", c.user, c.hash, err)
		}
	}
}

func (e *readEnv) activity(userID int64, hash string, before int64, limit int) ActivityPage {
	e.t.Helper()
	p, err := e.s.Activity(e.ctx, userID, hash, before, limit)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// groups описывает записи ленты как «hash/устройство:from→to×syncs».
func groups(gs []Group) string {
	var out []string
	for _, g := range gs {
		out = append(out, fmt.Sprintf("%s/%s:%g→%g×%d", g.Hash, str(g.DeviceName), g.FromPercentage, g.ToPercentage, g.Syncs))
	}
	return strings.Join(out, " ")
}

func TestActivityGroups(t *testing.T) {
	e := newReadEnv(t)
	alice := e.user("alice")
	pb := e.device("alice", "pocketbook")
	x4 := e.device("alice", "xteink")

	// Вечер: 21:05, 21:20, 21:40, 22:05 — одна запись; B вперемешку её не рвёт.
	e.sync(pb, "A", 0.518, 5*time.Minute, Metadata{Title: "Book A"})
	start := e.now
	e.sync(pb, "A", 0.55, 15*time.Minute, Metadata{})
	e.sync(pb, "B", 0.1, time.Minute, Metadata{})
	e.sync(pb, "A", 0.6, 19*time.Minute, Metadata{})
	e.sync(pb, "A", 0.624, 25*time.Minute, Metadata{})
	end := e.now
	// Ровно 30 минут — новая запись.
	e.sync(pb, "A", 0.63, 30*time.Minute, Metadata{})
	// Другое устройство через минуту — своя запись.
	e.sync(x4, "A", 0.64, time.Minute, Metadata{})
	// Офлайн-очередь: три события за секунды.
	e.sync(pb, "C", 0.2, 72*time.Hour, Metadata{})
	e.sync(pb, "C", 0.3, time.Second, Metadata{})
	e.sync(pb, "C", 0.35, time.Second, Metadata{})

	all := e.activity(alice, "", 0, 50)
	want := "C/pocketbook:0.2→0.35×3 A/xteink:0.64→0.64×1 A/pocketbook:0.63→0.63×1 " +
		"A/pocketbook:0.518→0.624×4 B/pocketbook:0.1→0.1×1"
	if got := groups(all.Items); got != want || all.Next != 0 {
		t.Fatalf("groups\n got %s\nwant %s (next %d)", got, want, all.Next)
	}
	g := all.Items[3]
	if !g.StartedAt.Equal(start) || !g.EndedAt.Equal(end) || str(g.Title) != "Book A" || g.ReportedDevice != "reader-alice" {
		t.Fatalf("evening group %+v", g)
	}

	if got := groups(e.activity(alice, "A", 0, 50).Items); got !=
		"A/xteink:0.64→0.64×1 A/pocketbook:0.63→0.63×1 A/pocketbook:0.518→0.624×4" {
		t.Fatalf("document filter: %s", got)
	}

	// Постранично по одной записи — те же записи, без дублей и разрывов.
	var paged []Group
	var before int64
	for i := 0; ; i++ {
		p := e.activity(alice, "", before, 1)
		paged = append(paged, p.Items...)
		if p.Next == 0 {
			break
		}
		if i > 10 {
			t.Fatal("pagination does not terminate")
		}
		before = p.Next
	}
	if got := groups(paged); got != want {
		t.Fatalf("paged\n got %s\nwant %s", got, want)
	}

	if _, err := e.s.Activity(e.ctx, e.user("bob"), "A", 0, 10); !errors.Is(err, ErrNoProgress) {
		t.Fatalf("foreign document: %v", err)
	}
	if p := e.activity(e.user("carol"), "", 0, 10); len(p.Items) != 0 || p.Next != 0 {
		t.Fatalf("empty feed %+v", p)
	}
}

// statBook добавляет книгу в статистику пользователя, как после разбора снимка.
func (e *readEnv) statBook(userID int64, hash, title, authors string, lastOpen int64) {
	e.t.Helper()
	db := e.s.db.Write
	if _, err := db.ExecContext(e.ctx, `INSERT INTO documents (hash, first_seen_at) VALUES (?, 0) ON CONFLICT DO NOTHING`, hash); err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.ExecContext(e.ctx, `
		INSERT INTO stat_books (user_id, document_id, title, authors, last_open)
		VALUES (?, (SELECT id FROM documents WHERE hash = ?), ?, nullif(?, ''), ?)`,
		userID, hash, title, authors, lastOpen); err != nil {
		e.t.Fatal(err)
	}
}

func TestTitleFromStatistics(t *testing.T) {
	e := newReadEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	pb := e.device("alice", "pocketbook")
	e.sync(pb, "stat", 0.3, time.Minute, Metadata{})
	e.sync(pb, "meta", 0.4, time.Minute, Metadata{Title: "From metadata"})
	e.sync(pb, "edited", 0.5, time.Minute, Metadata{})
	e.sync(pb, "foreign", 0.6, time.Minute, Metadata{})
	e.sync(pb, "unknown", 0.7, time.Minute, Metadata{})

	e.statBook(alice, "stat", "Stat title", "Stat author", 100)
	e.statBook(alice, "meta", "Other title", "Meta-less author", 100)
	// Метаданные книги правили в KOReader: две строки с одним хешем.
	e.statBook(alice, "edited", "Old title", "Old author", 100)
	e.statBook(alice, "edited", "New title", "", 200)
	e.statBook(bob, "foreign", "Bob's title", "Bob", 100)

	page := e.books(alice, FilterAll, 0, 50)
	got := map[string]Book{}
	for _, b := range page.Items {
		got[b.Hash] = b
	}
	check := func(hash, title, authors string) {
		t.Helper()
		b := got[hash]
		if str(b.Title) != title || str(b.Authors) != authors || b.Filename != nil {
			t.Errorf("%s: title %s, authors %s, filename %s", hash, str(b.Title), str(b.Authors), str(b.Filename))
		}
	}
	check("stat", "Stat title", "Stat author")
	check("meta", "From metadata", "Meta-less author")
	check("edited", "New title", "<nil>")
	check("foreign", "<nil>", "<nil>")
	check("unknown", "<nil>", "<nil>")

	if page.Counts.Unknown != 2 {
		t.Errorf("unknown count %d, want 2", page.Counts.Unknown)
	}
	if u := hashes(e.books(alice, FilterUnknown, 0, 50).Items); u != "unknown,foreign" {
		t.Errorf("unknown filter %s", u)
	}

	b, _, err := e.s.Book(e.ctx, alice, "stat")
	if err != nil || str(b.Title) != "Stat title" || str(b.Authors) != "Stat author" {
		t.Fatalf("book card: %+v %v", b, err)
	}
	a := e.activity(alice, "stat", 0, 10)
	if len(a.Items) != 1 || str(a.Items[0].Title) != "Stat title" || str(a.Items[0].Authors) != "Stat author" {
		t.Fatalf("activity: %+v", a.Items)
	}
}

func TestToday(t *testing.T) {
	e := newReadEnv(t)
	e.user("alice")
	e.user("bob")
	alice := e.device("alice", "Kobo")
	pb := e.device("alice", "PocketBook")
	bob := e.device("bob", "Kindle")
	at := func(y int, m time.Month, d, h, min int) {
		e.now = time.Date(y, m, d, h, min, 0, 0, time.UTC)
	}
	midnight := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	today := func(user int64, hash string, since time.Time, want float64) {
		t.Helper()
		got, err := e.s.Today(e.ctx, user, hash, since)
		if err != nil || math.Abs(got-want) > 1e-9 {
			t.Errorf("%s since %v: %v, %v; want %v", hash, since, got, err, want)
		}
	}

	at(2026, 11, 4, 20, 0)
	e.sync(alice, "evening", 0.583, 0, Metadata{})
	e.sync(alice, "yesterday", 0.3, 0, Metadata{})
	at(2026, 11, 5, 8, 0)
	// Устройства не различаются: база — последняя синхронизация до полуночи с любого.
	e.sync(pb, "evening", 0.601, 0, Metadata{})
	e.sync(alice, "evening", 0.624, 2*time.Hour, Metadata{})
	e.sync(alice, "started", 0.05, 0, Metadata{})
	e.sync(alice, "started", 0.12, time.Hour, Metadata{})

	userID := alice.UserID
	today(userID, "evening", midnight, 0.041)
	today(userID, "yesterday", midnight, 0)
	today(userID, "started", midnight, 0.07)

	// 21:30 UTC 3 ноября — уже 4 ноября в Москве.
	at(2026, 11, 3, 20, 0)
	e.sync(alice, "moscow", 0.10, 0, Metadata{})
	at(2026, 11, 3, 21, 30)
	e.sync(alice, "moscow", 0.15, 0, Metadata{})
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	today(userID, "moscow", time.Date(2026, 11, 4, 0, 0, 0, 0, msk), 0.05)
	today(userID, "moscow", time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), 0)

	e.sync(bob, "bobs", 0.5, 0, Metadata{})
	if _, err := e.s.Today(e.ctx, userID, "bobs", midnight); !errors.Is(err, ErrNoProgress) {
		t.Fatalf("foreign book: %v", err)
	}
}
