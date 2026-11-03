package hashing

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing/internal/synth"
)

func loadGolden(t *testing.T) []synth.Case {
	t.Helper()
	data, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cs []synth.Case
	if err := json.Unmarshal(data, &cs); err != nil {
		t.Fatal(err)
	}
	if len(cs) == 0 {
		t.Fatal("golden.json is empty")
	}
	return cs
}

// TestPartialMD5Golden сверяет хеш с эталоном, полученным прогоном цикла
// util.partialMD5 в LuaJIT (testdata/oracle.lua).
func TestPartialMD5Golden(t *testing.T) {
	dir := t.TempDir()
	for _, c := range loadGolden(t) {
		t.Run(c.Name, func(t *testing.T) {
			if c.Sparse && testing.Short() {
				t.Skip("sparse file > 1 GiB skipped in -short mode")
			}
			path := filepath.Join(dir, c.Name)
			if err := synth.Write(path, c); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			got, err := PartialMD5(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.MD5 {
				t.Fatalf("PartialMD5 = %s, want %s", got, c.MD5)
			}
		})
	}
}

func TestPartialMD5Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := PartialMD5(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Fatalf("got %s", got)
	}
}

func TestPartialMD5MissingFile(t *testing.T) {
	if _, err := PartialMD5(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error")
	}
}

// dirtyReader отдаёт данные из data, а при коротком чтении заполняет остаток
// буфера мусором: если реализация хеширует весь буфер, а не buf[:n], хеш изменится.
type dirtyReader struct {
	data []byte
	err  error // ошибка, которую вернуть при чтении со смещения errAt
	at   int64
}

func (r *dirtyReader) ReadAt(p []byte, off int64) (int, error) {
	if r.err != nil && off == r.at {
		return 0, r.err
	}
	for i := range p {
		p[i] = 0xAA
	}
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestPartialMD5ShortTail(t *testing.T) {
	data := make([]byte, 1024+300)
	synth.Fill(data, 0)
	// Сэмплы: [0,1024) и [1024,1324) — второй укорочен до 300 байт.
	want := md5.Sum(data)
	got, err := partialMD5(&dirtyReader{data: data})
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("got %s, want %x", got, want)
	}
}

func TestPartialMD5ReadError(t *testing.T) {
	data := make([]byte, 8192)
	boom := errors.New("connection reset")
	_, err := partialMD5(&dirtyReader{data: data, err: boom, at: 4096})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestFilenameMD5Variants(t *testing.T) {
	nfc := "Мой.epub"  // й как одна кодовая точка U+0439
	nfd := "Мой.epub" // и + combining breve
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"latin", "book.epub", []string{md5hex("book.epub")}},
		{"path stripped", "/mnt/ext1/Books/book.epub", []string{md5hex("book.epub")}},
		{"cyrillic NFC", nfc, []string{md5hex(nfc), md5hex(nfd)}},
		{"cyrillic NFD", nfd, []string{md5hex(nfd), md5hex(nfc)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilenameMD5Variants(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}
