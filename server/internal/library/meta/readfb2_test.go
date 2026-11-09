package meta

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func readFB2File(t *testing.T, path, format string) ([]byte, error) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return ReadFB2(f, fi.Size(), format)
}

func TestReadFB2(t *testing.T) {
	want, err := os.ReadFile("testdata/utf8-bom.fb2")
	if err != nil {
		t.Fatal(err)
	}
	got, err := readFB2File(t, "testdata/utf8-bom.fb2", "fb2")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("fb2: %d bytes, %v", len(got), err)
	}
	got, err = readFB2File(t, "testdata/piknik.fb2.zip", "fb2.zip")
	if err != nil || !bytes.Contains(got, []byte("<FictionBook")) {
		t.Fatalf("fb2.zip: %q, %v", got, err)
	}
}

func TestReadFB2Errors(t *testing.T) {
	if _, err := readFB2File(t, "testdata/epub2.epub", "epub"); err == nil {
		t.Error("epub accepted")
	}
	if _, err := readFB2File(t, "testdata/epub2.epub", "fb2.zip"); err == nil {
		t.Error("archive without .fb2 accepted")
	}
	if _, err := readFB2File(t, "testdata/garbage.fb2", "fb2.zip"); err == nil {
		t.Error("not a zip accepted")
	}
}

func TestReadFB2TooLarge(t *testing.T) {
	if _, err := ReadFB2(strings.NewReader(""), maxCoverRead+1, "fb2"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("fb2: %v", err)
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create("big.fb2")
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for range maxCoverRead >> 20 {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFB2(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "fb2.zip"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("fb2.zip: %v", err)
	}
}
