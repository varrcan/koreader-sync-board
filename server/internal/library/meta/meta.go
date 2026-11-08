// Package meta извлекает локальные метаданные книг (FB2, EPUB) и строит ключ
// группировки файлов в книгу.
package meta

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Book — метаданные одного файла. Пустая строка — значения нет. ISBN — только цифры
// (и X) с верной контрольной суммой, Description — простой текст с абзацами через
// пустую строку.
type Book struct {
	Title       string
	Authors     []string
	Series      string
	SeriesIndex *float64
	Language    string
	ISBN        string
	Description string
}

// maxRead — предел распакованных байтов, которые читаются из описания FB2 или OPF.
// Защищает от zip-бомб и от FB2, у которого до <description> мегабайты мусора.
const maxRead = 4 << 20

// ErrTooLarge — данные не уместились в предел чтения.
var ErrTooLarge = errors.New("data exceeds the size limit")

// Parse разбирает метаданные файла формата format (как в library_files.format).
// Для форматов без разбора возвращает пустую Book и nil.
func Parse(path, format string) (Book, error) {
	switch format {
	case "fb2":
		return parseFile(path, FB2)
	case "fb2.zip":
		return FB2Zip(path)
	case "epub":
		return EPUB(path)
	}
	return Book{}, nil
}

// ParseAt разбирает уже открытый файл: так вызывающий сам решает, как его открыть
// (Parse разыменовывает симлинки по пути).
func ParseAt(r io.ReaderAt, size int64, format string) (Book, error) {
	switch format {
	case "fb2":
		return FB2(io.NewSectionReader(r, 0, size))
	case "fb2.zip", "epub":
		z, err := zip.NewReader(r, size)
		if err != nil {
			return Book{}, err
		}
		if format == "epub" {
			return epubArchive(z)
		}
		return fb2Archive(z)
	}
	return Book{}, nil
}

func parseFile(path string, parse func(io.Reader) (Book, error)) (Book, error) {
	f, err := os.Open(path)
	if err != nil {
		return Book{}, err
	}
	defer f.Close()
	return parse(f)
}

// FB2Zip разбирает первую запись *.fb2 архива.
func FB2Zip(path string) (Book, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return Book{}, err
	}
	defer z.Close()
	return fb2Archive(&z.Reader)
}

func fb2Archive(z *zip.Reader) (Book, error) {
	f, err := fb2Entry(z)
	if err != nil {
		return Book{}, err
	}
	r, err := f.Open()
	if err != nil {
		return Book{}, err
	}
	defer r.Close()
	return FB2(r)
}

// fb2Entry — первая запись *.fb2 архива.
func fb2Entry(z *zip.Reader) (*zip.File, error) {
	for _, f := range z.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".fb2") {
			return f, nil
		}
	}
	return nil, errors.New("no .fb2 entry in archive")
}

// limited ограничивает r пределом и сообщает ErrTooLarge при его
// превышении, а не обычный EOF, после которого разбор упал бы с невнятной ошибкой.
type limited struct {
	r io.Reader
	n int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.n <= 0 {
		// Данные ровно по пределу — не превышение: проверяем, есть ли что-то дальше.
		var probe [1]byte
		if n, err := l.r.Read(probe[:]); n == 0 && errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		return 0, ErrTooLarge
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

func limit(r io.Reader, n int64) io.Reader { return &limited{r: r, n: n} }

// stripBOM снимает UTF-8 BOM: encoding/xml его не пропускает.
func stripBOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	return br
}

// clean схлопывает пробельные символы (в том числе &nbsp; и переводы строк).
func clean(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// parseIndex разбирает номер в серии; нечисловой номер отбрасывается.
func parseIndex(s string) *float64 {
	s = clean(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	if err != nil {
		return nil
	}
	return &v
}

// readAll читает r целиком в пределах n байт.
func readAll(r io.Reader, n int64) ([]byte, error) {
	b, err := io.ReadAll(limit(r, n))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return b, nil
}
