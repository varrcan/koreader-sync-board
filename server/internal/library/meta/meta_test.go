package meta

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func ptr(f float64) *float64 { return &f }

var piknikFB2 = Book{
	Title:       "Пикник на обочине",
	Authors:     []string{"Аркадий Натанович Стругацкий", "Борис Стругацкий", "Аноним"},
	Series:      "Мир Полудня",
	SeriesIndex: ptr(3),
	Language:    "ru",
}

func TestFB2Encodings(t *testing.T) {
	for _, name := range []string{"utf8-bom.fb2", "cp1251.fb2", "koi8r.fb2"} {
		got, err := Parse("testdata/"+name, "fb2")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, piknikFB2) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, piknikFB2)
		}
	}
}

func TestFB2Zip(t *testing.T) {
	got, err := Parse("testdata/piknik.fb2.zip", "fb2.zip")
	if err != nil {
		t.Fatal(err)
	}
	want := piknikFB2
	want.Authors = want.Authors[:2]
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestFB2Errors(t *testing.T) {
	for name, format := range map[string]string{
		"no-description.fb2": "fb2", "garbage.fb2": "fb2", "bomb.fb2.zip": "fb2.zip",
		"epub2.epub": "fb2.zip", "missing.fb2": "fb2",
	} {
		got, err := Parse("testdata/"+name, format)
		if err == nil {
			t.Errorf("%s: no error, got %+v", name, got)
		}
	}
	if _, err := Parse("testdata/bomb.fb2.zip", "fb2.zip"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("bomb: %v", err)
	}
}

// failAfter отдаёт head одним чтением, а следующее чтение — провал теста:
// разбор FB2 не должен заглядывать за </description>.
type failAfter struct {
	t    *testing.T
	head string
	done bool
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.done {
		f.t.Error("read past </description>")
		return 0, io.ErrUnexpectedEOF
	}
	f.done = true
	return copy(p, f.head), nil
}

func TestFB2StopsAtDescriptionEnd(t *testing.T) {
	head := `<?xml version="1.0" encoding="UTF-8"?><FictionBook><description><title-info>` +
		`<author><nickname> </nickname></author><book-title>  </book-title>` +
		`<sequence name="S" number="x"/></title-info></description>`
	got, err := FB2(&failAfter{t: t, head: head})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Book{Series: "S"}) {
		t.Errorf("got %+v", got)
	}
}

func TestEPUB(t *testing.T) {
	for name, want := range map[string]Book{
		"epub2.epub": {
			Title:   "Пикник на обочине.",
			Authors: []string{"Стругацкий Аркадий", "Стругацкий Борис"},
			Series:  "Мир Полудня", SeriesIndex: ptr(3), Language: "ru",
		},
		"epub3.epub": {
			Title:   "Roadside Picnic",
			Authors: []string{"Arkady Strugatsky", "Boris Strugatsky"},
			Series:  "Noon Universe", SeriesIndex: ptr(2.5), Language: "en",
		},
		"empty-meta.epub": {},
	} {
		got, err := Parse("testdata/"+name, "epub")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
	}
}

func TestEPUBErrors(t *testing.T) {
	for _, name := range []string{"no-container.epub", "utf8-bom.fb2", "missing.epub"} {
		if got, err := Parse("testdata/"+name, "epub"); err == nil {
			t.Errorf("%s: no error, got %+v", name, got)
		}
	}
	if _, err := parseOPF([]byte("<<not xml")); err == nil {
		t.Error("garbage OPF: no error")
	}
}

func TestReadAllLimit(t *testing.T) {
	if b, err := readAll(strings.NewReader(strings.Repeat("x", maxRead)), maxRead); err != nil || len(b) != maxRead {
		t.Fatalf("exactly at the limit: %d bytes, %v", len(b), err)
	}
	if _, err := readAll(strings.NewReader(strings.Repeat("x", maxRead+1)), maxRead); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
}

func TestParseOtherFormats(t *testing.T) {
	got, err := Parse("testdata/missing.pdf", "pdf")
	if err != nil || !reflect.DeepEqual(got, Book{}) {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestKey(t *testing.T) {
	strug := []string{"Аркадий Стругацкий", "Борис Стругацкий"}
	base, ok := Key("Пикник на обочине", strug)
	if !ok {
		t.Fatal("no key")
	}
	same := []struct {
		title   string
		authors []string
	}{
		{"Пикник на обочине.", strug},
		{"ПИКНИК  НА\tОБОЧИНЕ", strug},
		{"Пикник на обочине", []string{"Стругацкий Борис", "Стругацкий, Аркадий"}},
		{"Пикник на обочине", []string{"Борис Стругацкий", "", "Аркадий Стругацкий"}},
	}
	for _, s := range same {
		if k, _ := Key(s.title, s.authors); k != base {
			t.Errorf("%q %q: key %q, want %q", s.title, s.authors, k, base)
		}
	}
	if a, b := mustKey(t, "Ёжик в тумане", nil), mustKey(t, "ежик в тумане", nil); a != b {
		t.Errorf("ё: %q != %q", a, b)
	}
	// NFKC: лигатура и полноширинные цифры; NFD-«й» совпадает с составной.
	if a, b := mustKey(t, "ﬁnal １２", nil), mustKey(t, "final 12", nil); a != b {
		t.Errorf("NFKC: %q != %q", a, b)
	}
	if a, b := mustKey(t, "Мои\u0306", nil), mustKey(t, "Мой", nil); a != b {
		t.Errorf("NFD: %q != %q", a, b)
	}
	if k, _ := Key("Пикник на обочине", []string{"Аркадий Натанович Стругацкий", "Борис Стругацкий"}); k == base {
		t.Error("different authors share a key")
	}
	if k, _ := Key("Пикник на обочине", []string{"Другой Автор"}); k == base {
		t.Error("different authors share a key")
	}
	for _, title := range []string{"", "  ", "…!"} {
		if _, ok := Key(title, strug); ok {
			t.Errorf("%q: key for empty title", title)
		}
	}
	if !strings.HasSuffix(mustKey(t, "Стихи", nil), "\x1f") {
		t.Error("title without authors")
	}
}

func mustKey(t *testing.T, title string, authors []string) string {
	t.Helper()
	k, ok := Key(title, authors)
	if !ok {
		t.Fatalf("%q: no key", title)
	}
	return k
}

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"Пикник  на обочине.": "пикник на обочине",
		"ЁЛКА":                "елка",
		"Ёлка: палка—2":       "елка палка 2",
		"ﬁ-Café":              "fi café",
		" ... ":               "",
	} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}
