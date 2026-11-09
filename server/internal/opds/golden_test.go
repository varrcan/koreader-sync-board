package opds

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"flag"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library"
)

var update = flag.Bool("update", false, "перезаписать testdata/feed.xml")

const feedFile = "testdata/feed.xml"

// goldenFeed собирает одним документом всё, что бывает в лентах: навигацию, ссылки
// обоих видов поиска, next с «&» в запросе и записи со всеми видами ссылок.
func goldenFeed() feed {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	text := func(s string) *string { return &s }
	cover := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	page := library.FeedPage{Next: 7, Items: []library.FeedBook{
		{
			Book: library.Book{ID: 12, Title: text(`Пикник & <другие> "рассказы" 'ранние'`),
				Authors: text("Стругацкий, Аркадий, Стругацкий, Борис"), Filename: "piknik.fb2", Cover: &cover},
			Files:       []library.FeedFile{{ID: 31, Path: "piknik.fb2", Format: "fb2", Size: 2048}, {ID: 32, Path: "piknik.fb2.zip", Format: "fb2.zip", Size: 1024}},
			Description: text("Первая строка\nвторая <b>строка</b> & «ёлка»\x01 конец"),
			Updated:     now,
		},
		{
			Book:    library.Book{ID: 9, Filename: "Война и мир.epub"},
			Files:   []library.FeedFile{{ID: 20, Path: "Война и мир.epub", Format: "epub", Size: 4096}, {ID: 21, Path: "a.pdf", Format: "pdf", Size: 1}},
			Updated: now,
		},
	}}
	m := feedMeta{id: "urn:ksb:opds:search", title: "Search results", path: Prefix + "/search", query: url.Values{"q": {"a&b ё"}}}
	f := bookFeed(m, page, now)
	root := rootFeed(now)
	f.Links = append(root.Links, f.Links[len(f.Links)-1])
	f.Entries = append(root.Entries, f.Entries...)
	return f
}

func TestFeedSnapshot(t *testing.T) {
	got, err := marshal(goldenFeed())
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(feedFile, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(feedFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("лента отличается от %s; если так и задумано — task opds:golden\ngot:\n%s", feedFile, got)
	}
}

func linkView(links []link) []any {
	out := make([]any, 0, len(links))
	for _, l := range links {
		v := map[string]any{"rel": l.Rel, "href": l.Href, "type": l.Type}
		if l.Title != "" {
			v["title"] = l.Title
		}
		if l.Length != 0 {
			v["length"] = strconv.FormatInt(l.Length, 10)
		}
		out = append(out, v)
	}
	return out
}

// koreaderView — та же лента в виде таблицы OPDSParser:parse.
func koreaderView(f feed) map[string]any {
	entries := make([]any, 0, len(f.Entries))
	for _, e := range f.Entries {
		v := map[string]any{"title": e.Title, "id": e.ID, "updated": e.Updated, "link": linkView(e.Links)}
		if e.Author != nil {
			v["author"] = []any{map[string]any{"name": e.Author.Name}}
		}
		if e.Summary != "" {
			v["summary"] = e.Summary
		}
		entries = append(entries, v)
	}
	return map[string]any{"xmlns": "http://www.w3.org/2005/Atom", "id": f.ID, "title": f.Title, "updated": f.Updated,
		"link": linkView(f.Links), "entry": entries}
}

// Настоящий парсер KOReader прочитал снимок так же, как encoding/xml: golden.json
// получен из него под LuaJIT (task opds:golden), тест LuaJIT не требует.
func TestKOReaderParser(t *testing.T) {
	raw, err := os.ReadFile(feedFile)
	if err != nil {
		t.Fatal(err)
	}
	var f feed
	if err := xml.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Feed map[string]any `json:"feed"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	if want := koreaderView(f); !reflect.DeepEqual(golden.Feed, want) {
		a, _ := json.MarshalIndent(golden.Feed, "", " ")
		b, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("KOReader:\n%s\nexpected:\n%s", a, b)
	}
}
