package library

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/image/draw"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

const (
	thumbWidth   = 384
	thumbHeight  = 576
	thumbQuality = 80
	// maxCoverPixels держит худший случай около 400 МиБ: progressive CMYK в
	// image/jpeg занимает 24 байта на пиксель.
	maxCoverPixels = 16_000_000
)

var (
	// ErrCoverNotFound — миниатюры нет или идентификатор не является её хешем.
	ErrCoverNotFound = errors.New("cover not found")

	errCoverBytes  = errors.New("cover source exceeds the size limit")
	errCoverPixels = errors.New("cover source exceeds the pixel limit")

	coverID = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Thumbnail строит JPEG-миниатюру, вписанную в 384×576, без увеличения. Форматы
// источника регистрирует пакет meta. Любая ошибка означает, что обложки нет.
func Thumbnail(src []byte) (thumb []byte, err error) {
	// В image/jpeg бывали паники на испорченных данных (golang/go#78368), а паника
	// в горутине сканера уронила бы весь сервер вместе с kosync.
	defer func() {
		if r := recover(); r != nil {
			thumb, err = nil, fmt.Errorf("cover decoder panic: %v", r)
		}
	}()
	if len(src) > meta.MaxCover {
		return nil, errCoverBytes
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxCoverPixels {
		return nil, errCoverPixels
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	w, h := fitThumb(img.Bounds().Dx(), img.Bounds().Dy())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// Без заливки прозрачность в JPEG становится чёрной.
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	// BiLinear расширяет ядро при уменьшении и даёт антиалиасинг, а по качеству на
	// обложках не уступает CatmullRom при вдвое меньшем времени.
	draw.BiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: thumbQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func fitThumb(w, h int) (int, int) {
	if w <= thumbWidth && h <= thumbHeight {
		return w, h
	}
	scale := min(float64(thumbWidth)/float64(w), float64(thumbHeight)/float64(h))
	return max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
}

// Covers — каталог миниатюр. Имя файла — хеш содержимого, поэтому URL неизменяем
// и одинаковые обложки хранятся один раз. Пишет только горутина сканера.
type Covers struct {
	dir string
}

// NewCovers создаёт хранилище; каталог появится при первой записи.
func NewCovers(dir string) *Covers { return &Covers{dir: dir} }

func (c *Covers) path(id string) string { return filepath.Join(c.dir, id+".jpg") }

// save записывает миниатюру через временный файл и возвращает её идентификатор.
func (c *Covers) save(thumb []byte) (string, error) {
	sum := sha256.Sum256(thumb)
	id := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(c.dir, "tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(thumb); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return id, os.Rename(tmp.Name(), c.path(id))
}

// Open открывает миниатюру. Идентификатор приходит из URL: проверка не пускает к
// диску ничего, кроме хеша.
func (c *Covers) Open(id string) (*os.File, error) {
	if !coverID.MatchString(id) {
		return nil, ErrCoverNotFound
	}
	f, err := os.Open(c.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrCoverNotFound
	}
	return f, err
}

// prune удаляет всё, что не входит в used, включая временные файлы после сбоя.
func (c *Covers) prune(used map[string]bool) error {
	entries, err := os.ReadDir(c.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if id, ok := cutJPG(e.Name()); ok && used[id] {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func cutJPG(name string) (string, bool) {
	if filepath.Ext(name) != ".jpg" {
		return "", false
	}
	return name[:len(name)-len(".jpg")], true
}
