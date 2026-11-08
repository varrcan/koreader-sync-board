package library

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

func encodeJPEG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeThumb(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("thumbnail is not a JPEG: %v", err)
	}
	return img
}

func TestThumbnailSize(t *testing.T) {
	for _, tt := range []struct{ w, h, wantW, wantH int }{
		{2000, 3000, 384, 576},
		{200, 300, 200, 300},
		{3000, 1000, 384, 128},
		{1000, 4000, 144, 576},
		{4000, 1, 384, 1},
	} {
		thumb, err := Thumbnail(encodeJPEG(t, tt.w, tt.h, color.Gray{128}))
		if err != nil {
			t.Fatalf("%dx%d: %v", tt.w, tt.h, err)
		}
		if b := decodeThumb(t, thumb).Bounds(); b.Dx() != tt.wantW || b.Dy() != tt.wantH {
			t.Errorf("%dx%d: got %dx%d, want %dx%d", tt.w, tt.h, b.Dx(), b.Dy(), tt.wantW, tt.wantH)
		}
	}
}

func TestThumbnailTransparentPNG(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 20, 30))); err != nil {
		t.Fatal(err)
	}
	thumb, err := Thumbnail(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decodeThumb(t, thumb).At(10, 15).RGBA()
	if r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("background is (%d, %d, %d), want white", r>>8, g>>8, b>>8)
	}
}

func TestThumbnailFormats(t *testing.T) {
	for _, name := range []string{"cmyk.jpeg", "cover.webp"} {
		src, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		thumb, err := Thumbnail(src)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		decodeThumb(t, thumb)
	}
}

// pngHeader — сигнатура и IHDR: размеры объявлены, данных нет.
func pngHeader(w, h uint32) []byte {
	buf := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0d")
	body := []byte("IHDR")
	body = binary.BigEndian.AppendUint32(body, w)
	body = binary.BigEndian.AppendUint32(body, h)
	body = append(body, 8, 6, 0, 0, 0)
	buf = append(buf, body...)
	return binary.BigEndian.AppendUint32(buf, crc32.ChecksumIEEE(body))
}

func TestThumbnailLimits(t *testing.T) {
	if _, err := Thumbnail(pngHeader(30000, 30000)); !errors.Is(err, errCoverPixels) {
		t.Errorf("30000x30000: %v", err)
	}
	if _, err := Thumbnail(pngHeader(4001, 4000)); !errors.Is(err, errCoverPixels) {
		t.Errorf("just over the limit: %v", err)
	}
	// Ровно 16 Мп проходят проверку и падают уже на отсутствующих данных.
	if _, err := Thumbnail(pngHeader(4000, 4000)); err == nil || errors.Is(err, errCoverPixels) {
		t.Errorf("exactly at the limit: %v", err)
	}
	if _, err := Thumbnail(make([]byte, meta.MaxCover+1)); !errors.Is(err, errCoverBytes) {
		t.Errorf("over 32 MiB: %v", err)
	}
}

func init() {
	image.RegisterFormat("panics", "PANICS", func(io.Reader) (image.Image, error) {
		panic("decoder bug")
	}, func(io.Reader) (image.Config, error) {
		return image.Config{Width: 1, Height: 1}, nil
	})
}

func TestThumbnailBroken(t *testing.T) {
	good := encodeJPEG(t, 100, 100, color.Gray{128})
	for name, src := range map[string][]byte{
		"garbage":   []byte("not an image"),
		"svg":       []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		"truncated": good[:len(good)/2],
		"panic":     []byte("PANICS"),
	} {
		if thumb, err := Thumbnail(src); err == nil {
			t.Errorf("%s: no error, %d bytes", name, len(thumb))
		}
	}
}

func TestCoversSaveOpen(t *testing.T) {
	c := NewCovers(filepath.Join(t.TempDir(), "nested", "covers"))
	id, err := c.save([]byte("jpeg"))
	if err != nil {
		t.Fatal(err)
	}
	if !coverID.MatchString(id) {
		t.Fatalf("id %q", id)
	}
	again, err := c.save([]byte("jpeg"))
	if err != nil || again != id {
		t.Fatalf("same content: %q, %v", again, err)
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries %v, %v", entries, err)
	}
	f, err := c.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if b, _ := io.ReadAll(f); string(b) != "jpeg" {
		t.Errorf("content %q", b)
	}
	for _, bad := range []string{"", "../ksb.sqlite3", strings.Repeat("A", 64), strings.Repeat("0", 64)} {
		if _, err := c.Open(bad); !errors.Is(err, ErrCoverNotFound) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestCoversPrune(t *testing.T) {
	c := NewCovers(t.TempDir())
	keep, err := c.save([]byte("keep"))
	if err != nil {
		t.Fatal(err)
	}
	drop, err := c.save([]byte("drop"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(c.dir, "tmp-123"), "partial")
	if err := c.prune(map[string]bool{keep: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Open(keep); err != nil {
		t.Errorf("kept file: %v", err)
	}
	if _, err := c.Open(drop); !errors.Is(err, ErrCoverNotFound) {
		t.Errorf("dropped file: %v", err)
	}
	if entries, _ := os.ReadDir(c.dir); len(entries) != 1 {
		t.Errorf("entries %v", entries)
	}
	if err := NewCovers(filepath.Join(c.dir, "missing")).prune(nil); err != nil {
		t.Errorf("missing directory: %v", err)
	}
}
