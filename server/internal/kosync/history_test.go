package kosync

import (
	"testing"
	"time"
)

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestHistoryAppendOnly(t *testing.T) {
	e := newEnv(t, Options{})
	for i, p := range []string{"0.1", "0.2", "0.3"} {
		body := `{"document":"abc123","progress":"p` + p + `","percentage":` + p + `,"device":"Kindle","device_id":"X1"}`
		e.check("put", req{method: "PUT", path: "/syncs/progress", body: body, as: "alice/Kindle"}, 200, nil)
		if got := e.count(`SELECT count(*) FROM progress_events`); got != i+1 {
			t.Fatalf("events = %d after %d updates", got, i+1)
		}
	}
	got := e.do(req{method: "GET", path: "/syncs/progress/abc123", as: "alice/Kindle"}).body.(map[string]any)
	if got["percentage"] != 0.3 || got["progress"] != "p0.3" {
		t.Fatalf("GET = %v", got)
	}
	if n := e.count(`SELECT count(*) FROM progress_current`); n != 1 {
		t.Fatalf("progress_current rows = %d", n)
	}
}

func TestRejectedWritesNothing(t *testing.T) {
	e := newEnv(t, Options{})
	bad := []req{
		{method: "PUT", path: "/syncs/progress", body: goodPut},
		{method: "PUT", path: "/syncs/progress", body: goodPut, noAccept: true, as: "alice/Kindle"},
		{method: "PUT", path: "/syncs/progress", body: `{"document":"ab-c","progress":"p","percentage":0.1,"device":"d"}`, as: "alice/Kindle"},
		{method: "PUT", path: "/syncs/progress", body: `{"document":"abc","percentage":0.1,"device":"d"}`, as: "alice/Kindle"},
		{method: "PUT", path: "/syncs/progress", body: `{`, as: "alice/Kindle"},
	}
	for _, r := range bad {
		if got := e.do(r); got.status == 200 {
			t.Fatalf("%s accepted", r.body)
		}
	}
	if n := e.count(`SELECT (SELECT count(*) FROM progress_events) + (SELECT count(*) FROM documents) + (SELECT count(*) FROM progress_current)`); n != 0 {
		t.Fatalf("%d rows written by rejected requests", n)
	}
}

func TestSharedRegistry(t *testing.T) {
	e := newEnv(t, Options{})
	e.check("alice", req{method: "PUT", path: "/syncs/progress", body: goodPut, as: "alice/Kindle"}, 200, nil)
	first := now.Unix()
	now = now.Add(time.Hour)
	defer func() { now = now.Add(-time.Hour) }()
	e.check("bob", req{method: "PUT", path: "/syncs/progress", body: `{"document":"abc123","progress":"x","percentage":0.9,"device":"Kobo"}`, as: "bob/Kobo"}, 200, nil)

	if n := e.count(`SELECT count(*) FROM documents`); n != 1 {
		t.Fatalf("documents = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM documents WHERE hash = 'abc123' AND first_seen_at = ?`, first); n != 1 {
		t.Fatal("first_seen_at is not the first appearance")
	}
	// В реестре только хеш и время появления — никаких данных чтения.
	if n := e.count(`SELECT count(*) FROM pragma_table_info('documents')`); n != 3 {
		t.Fatalf("documents has %d columns", n)
	}
	a := e.do(req{method: "GET", path: "/syncs/progress/abc123", as: "alice/Kindle"}).body.(map[string]any)
	b := e.do(req{method: "GET", path: "/syncs/progress/abc123", as: "bob/Kindle"}).body.(map[string]any)
	if a["percentage"] != 0.42 || b["percentage"] != 0.9 {
		t.Fatalf("alice %v, bob %v", a, b)
	}
}

func TestMetadataPerUser(t *testing.T) {
	e := newEnv(t, Options{})
	put := func(as, meta string) {
		t.Helper()
		body := `{"document":"abc123","progress":"p","percentage":0.1,"device":"d","metadata":` + meta + `}`
		e.check("put "+meta, req{method: "PUT", path: "/syncs/progress", body: body, as: as}, 200, nil)
	}
	meta := func(user string) (string, string, string) {
		t.Helper()
		var f, ti, a string
		err := e.db.Read.QueryRow(`
			SELECT coalesce(meta_filename, ''), coalesce(meta_title, ''), coalesce(meta_authors, '')
			FROM progress_current c JOIN users u ON u.id = c.user_id WHERE u.username = ?`, user).Scan(&f, &ti, &a)
		if err != nil {
			t.Fatal(err)
		}
		return f, ti, a
	}
	put("alice/Kindle", `{"filename":"dune.fb2","title":"Дюна","authors":"Фрэнк Герберт"}`)
	if f, ti, a := meta("alice"); f != "dune.fb2" || ti != "Дюна" || a != "Фрэнк Герберт" {
		t.Fatalf("alice meta %q %q %q", f, ti, a)
	}
	put("alice/Kindle", `{"filename":"dune.fb2","title":""}`)
	if _, ti, _ := meta("alice"); ti != "Дюна" {
		t.Fatalf("empty title overwrote: %q", ti)
	}
	put("bob/Kindle", `{"title":"Dune"}`)
	if _, ti, _ := meta("bob"); ti != "Dune" {
		t.Fatalf("bob title %q", ti)
	}
	if _, ti, _ := meta("alice"); ti != "Дюна" {
		t.Fatalf("alice title changed to %q", ti)
	}
	put("alice/Kindle", `"not an object"`)
	if n := e.count(`SELECT count(*) FROM progress_events WHERE meta_json IS NULL`); n != 1 {
		t.Fatalf("events without meta = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM progress_events WHERE meta_json LIKE '%Дюна%'`); n != 1 {
		t.Fatalf("raw metadata not stored in event")
	}
}

func TestEventTimeIsReceiveTime(t *testing.T) {
	e := newEnv(t, Options{})
	e.check("today", req{method: "PUT", path: "/syncs/progress", body: `{"document":"abc123","progress":"new","percentage":0.8,"device":"d","timestamp":1}`, as: "alice/Kindle"}, 200, nil)
	// Через сутки приходит более старое обновление из офлайн-очереди: оно
	// становится текущим и получает время получения, как у эталона.
	now = now.Add(24 * time.Hour)
	defer func() { now = now.Add(-24 * time.Hour) }()
	e.check("queued", req{method: "PUT", path: "/syncs/progress", body: `{"document":"abc123","progress":"old","percentage":0.5,"device":"d"}`, as: "alice/Kobo"}, 200,
		map[string]any{"document": "abc123", "timestamp": float64(now.Unix())})
	got := e.do(req{method: "GET", path: "/syncs/progress/abc123", as: "alice/Kindle"}).body.(map[string]any)
	if got["progress"] != "old" || got["timestamp"] != float64(now.Unix()) {
		t.Fatalf("GET = %v", got)
	}
	if n := e.count(`SELECT count(*) FROM progress_events WHERE server_ts = ?`, now.Unix()); n != 1 {
		t.Fatal("server_ts is not receive time")
	}
}
