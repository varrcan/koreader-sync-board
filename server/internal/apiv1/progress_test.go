package apiv1

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
)

// reader — устройство пользователя, которое шлёт прогресс в обход kosync.
type reader struct {
	e *env
	p accounts.Principal
}

func (e *env) reader(user, name string) reader {
	e.t.Helper()
	_, pass, err := e.acc.AddDevice(e.ctx, user, name)
	if err != nil {
		e.t.Fatal(err)
	}
	p, err := e.acc.Authenticate(e.ctx, user, accounts.KeyFromPassword(pass), client)
	if err != nil {
		e.t.Fatal(err)
	}
	return reader{e: e, p: p}
}

func (r reader) sync(doc string, pct float64, at time.Time, title string) {
	r.e.t.Helper()
	if err := progress.New(r.e.db).Save(r.e.ctx, r.p, progress.Update{
		Document: doc, Percentage: pct, Progress: "1", Device: "PocketBook 740",
		Meta: progress.Metadata{Title: title}, ReceivedAt: at,
	}); err != nil {
		r.e.t.Fatal(err)
	}
}

func itemHashes(r resp) string {
	items, _ := r.body["items"].([]any)
	var out []string
	for _, it := range items {
		out = append(out, it.(map[string]any)["hash"].(string))
	}
	return strings.Join(out, ",")
}

var t0 = time.Date(2026, 11, 5, 21, 5, 0, 0, time.FixedZone("MSK", 3*3600))

func TestProgressWithoutSession(t *testing.T) {
	b := newEnv(t).browser()
	for _, p := range []string{"/api/v1/books", "/api/v1/books/abc", "/api/v1/activity"} {
		b.do("GET", p, nil).expect(t, p, 401, "unauthorized")
	}
}

func TestBooksAPI(t *testing.T) {
	e := newEnv(t)
	alice := e.panelUser("alice", accounts.RoleUser)
	admin := e.panelUser("root", accounts.RoleAdmin)
	pb := e.reader("alice", "pocketbook")
	pb.sync("aaa", 0.4, t0, "Book A")
	pb.sync("bbb", 0.2, t0.Add(time.Minute), "")
	pb.sync("ccc", 0.9996, t0.Add(2*time.Minute), "Book C")
	e.reader("root", "kobo").sync("root-book", 0.1, t0, "Admin book")

	r := alice.do("GET", "/api/v1/books", nil)
	r.expect(t, "list", 200, "")
	if itemHashes(r) != "ccc,bbb,aaa" || r.body["next_cursor"] != nil {
		t.Fatalf("list %v", r.body)
	}
	want := map[string]any{"all": 3.0, "reading": 2.0, "finished": 1.0, "unknown": 1.0}
	if fmt.Sprint(r.body["counts"]) != fmt.Sprint(want) {
		t.Fatalf("counts %v", r.body["counts"])
	}
	c := r.body["items"].([]any)[0].(map[string]any)
	if c["title"] != "Book C" || c["authors"] != nil || c["filename"] != nil || c["finished"] != true ||
		c["percentage"] != 0.9996 || c["device_name"] != "pocketbook" || c["reported_device"] != "PocketBook 740" ||
		c["updated_at"] != "2026-11-05T18:07:00Z" {
		t.Fatalf("book %v", c)
	}

	r = alice.do("GET", "/api/v1/books?filter=finished", nil)
	if itemHashes(r) != "ccc" || fmt.Sprint(r.body["counts"]) != fmt.Sprint(want) {
		t.Fatalf("finished %v", r.body)
	}
	if got := itemHashes(alice.do("GET", "/api/v1/books?filter=unknown", nil)); got != "bbb" {
		t.Fatalf("unknown %s", got)
	}

	// Страницы.
	r = alice.do("GET", "/api/v1/books?limit=2", nil)
	next, _ := r.body["next_cursor"].(string)
	if itemHashes(r) != "ccc,bbb" || next == "" {
		t.Fatalf("page 1 %v", r.body)
	}
	r = alice.do("GET", "/api/v1/books?limit=2&cursor="+url.QueryEscape(next), nil)
	if itemHashes(r) != "aaa" || r.body["next_cursor"] != nil {
		t.Fatalf("page 2 %v", r.body)
	}

	for _, q := range []string{"filter=deleted", "limit=0", "limit=101", "limit=x", "cursor=abc", "cursor=-1", "cursor=0"} {
		alice.do("GET", "/api/v1/books?"+q, nil).expect(t, q, 400, "bad_request")
	}

	// Администратор видит только своё.
	if got := itemHashes(admin.do("GET", "/api/v1/books", nil)); got != "root-book" {
		t.Fatalf("admin books %s", got)
	}
	admin.do("GET", "/api/v1/books/aaa", nil).expect(t, "admin, alice's book", 404, "not_found")
	alice.do("GET", "/api/v1/books/missing", nil).expect(t, "missing", 404, "not_found")
	if r := alice.do("GET", "/api/v1/books", nil); r.body["items"] == nil {
		t.Fatal("items must be an array")
	}
	if r := e.panelUser("empty", accounts.RoleUser).do("GET", "/api/v1/books", nil); fmt.Sprint(r.body["items"]) != "[]" {
		t.Fatalf("empty list %v", r.body)
	}
}

func TestBookAPI(t *testing.T) {
	e := newEnv(t)
	alice := e.panelUser("alice", accounts.RoleUser)
	e.panelUser("bob", accounts.RoleUser)
	pb := e.reader("alice", "pocketbook")
	x4 := e.reader("alice", "xteink")
	pb.sync("hhh", 0.4, t0, "Book")
	x4.sync("hhh", 0.45, t0.Add(time.Hour), "")

	r := alice.do("GET", "/api/v1/books/hhh", nil)
	r.expect(t, "book", 200, "")
	b := r.body["book"].(map[string]any)
	ds := r.body["devices"].([]any)
	if b["percentage"] != 0.45 || b["title"] != "Book" || len(ds) != 2 {
		t.Fatalf("book %v", r.body)
	}
	d0, d1 := ds[0].(map[string]any), ds[1].(map[string]any)
	if d0["device_name"] != "xteink" || d0["percentage"] != 0.45 || d0["updated_at"] != "2026-11-05T19:05:00Z" ||
		d1["device_name"] != "pocketbook" || d1["percentage"] != 0.4 {
		t.Fatalf("devices %v", ds)
	}

	bob := e.browser()
	bob.login("bob", "bob-password").expect(t, "login bob", 200, "")
	bob.do("GET", "/api/v1/books/hhh", nil).expect(t, "bob, alice's book", 404, "not_found")
}

func TestActivityAPI(t *testing.T) {
	e := newEnv(t)
	alice := e.panelUser("alice", accounts.RoleUser)
	admin := e.panelUser("root", accounts.RoleAdmin)
	pb := e.reader("alice", "pocketbook")
	for i, m := range []int{0, 15, 35, 60} {
		pb.sync("aaa", 0.518+float64(i)*0.0353, t0.Add(time.Duration(m)*time.Minute), "Book A")
	}
	pb.sync("bbb", 0.1, t0.Add(3*time.Hour), "")

	r := alice.do("GET", "/api/v1/activity", nil)
	r.expect(t, "activity", 200, "")
	if itemHashes(r) != "bbb,aaa" || r.body["next_cursor"] != nil {
		t.Fatalf("activity %v", r.body)
	}
	a := r.body["items"].([]any)[1].(map[string]any)
	if a["syncs"] != 4.0 || a["from_percentage"] != 0.518 || a["started_at"] != "2026-11-05T18:05:00Z" ||
		a["ended_at"] != "2026-11-05T19:05:00Z" || a["title"] != "Book A" || a["device_name"] != "pocketbook" {
		t.Fatalf("group %v", a)
	}

	r = alice.do("GET", "/api/v1/activity?limit=1", nil)
	next, _ := r.body["next_cursor"].(string)
	if itemHashes(r) != "bbb" || next == "" {
		t.Fatalf("page 1 %v", r.body)
	}
	if r := alice.do("GET", "/api/v1/activity?limit=1&cursor="+next, nil); itemHashes(r) != "aaa" || r.body["next_cursor"] != nil {
		t.Fatalf("page 2 %v", r.body)
	}

	if got := itemHashes(alice.do("GET", "/api/v1/activity?document=aaa", nil)); got != "aaa" {
		t.Fatalf("document filter %s", got)
	}
	admin.do("GET", "/api/v1/activity?document=aaa", nil).expect(t, "admin, alice's document", 404, "not_found")
	if r := admin.do("GET", "/api/v1/activity", nil); fmt.Sprint(r.body["items"]) != "[]" {
		t.Fatalf("admin activity %v", r.body)
	}
	for _, q := range []string{"limit=0", "cursor=x", "document="} {
		alice.do("GET", "/api/v1/activity?"+q, nil).expect(t, q, 400, "bad_request")
	}
}
