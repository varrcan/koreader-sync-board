package meta

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // формат обложки
	_ "image/jpeg" // формат обложки
	_ "image/png"  // формат обложки
	"io"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strings"

	_ "golang.org/x/image/webp" // формат обложки
)

const (
	// MaxCover — предел байтов изображения обложки.
	MaxCover = 32 << 20
	// maxCoverRead — предел распакованного FB2: картинка лежит в конце файла, и
	// читать приходится всё, что перед ней.
	maxCoverRead = 128 << 20
	// maxCoverPage — предел документа spine, который считается страницей обложки.
	maxCoverPage = 5000
	// maxCoverDepth ограничивает вложенность тегов FB2: encoding/xml держит стек
	// открытых элементов, и 128 МиБ из одних «<a>» занимают в памяти больше 4 ГиБ.
	maxCoverDepth = 1024
)

var errCoverDepth = errors.New("fb2: elements are nested too deeply")

// Cover возвращает байты изображения обложки файла формата format или nil, если
// обложки нет. Ошибка *fs.PathError означает сбой чтения файла (стоит повторить),
// любая другая — что файл такой, какой есть.
func Cover(r io.ReaderAt, size int64, format string) ([]byte, error) {
	switch format {
	case "fb2":
		return fb2Cover(io.NewSectionReader(r, 0, size), maxCoverRead)
	case "fb2.zip":
		z, err := zip.NewReader(r, size)
		if err != nil {
			return nil, err
		}
		f, err := fb2Entry(z)
		if err != nil {
			return nil, err
		}
		if f.UncompressedSize64 > maxCoverRead {
			return nil, fmt.Errorf("%s: %w", f.Name, ErrTooLarge)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return fb2Cover(rc, maxCoverRead)
	case "epub":
		z, err := zip.NewReader(r, size)
		if err != nil {
			return nil, err
		}
		return epubCover(z)
	}
	return nil, nil
}

// fb2Cover находит в <description> ссылку на обложку и продолжает чтение до её
// <binary>. Без coverpage файл не читается дальше </description>.
func fb2Cover(r io.Reader, maxBytes int64) ([]byte, error) {
	d := newXMLDecoder(limit(stripBOM(r), maxBytes))
	var (
		stack         []string
		title, source string
	)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			if len(stack) > maxCoverDepth {
				return nil, errCoverDepth
			}
			if len(stack) != 5 || stack[1] != "description" || stack[3] != "coverpage" || t.Name.Local != "image" {
				continue
			}
			// Сравнение по локальному имени: encoding/xml даёт разный Space для
			// объявленного и необъявленного префикса.
			switch {
			case stack[2] == "title-info" && title == "":
				title = attr(t, "href")
			case stack[2] == "src-title-info" && source == "":
				source = attr(t, "href")
			}
		case xml.EndElement:
			i := len(stack) - 1
			for i >= 0 && stack[i] != t.Name.Local {
				i--
			}
			if i < 0 {
				continue
			}
			stack = stack[:i]
			if t.Name.Local != "description" || len(stack) != 1 {
				continue
			}
			ref := title
			if ref == "" {
				ref = source
			}
			id, ok := strings.CutPrefix(ref, "#")
			if !ok || id == "" {
				return nil, nil
			}
			return fb2Binary(d, id)
		}
	}
}

// fb2Binary читает до <binary> с нужным id и декодирует его содержимое.
func fb2Binary(d *xml.Decoder, id string) ([]byte, error) {
	depth := 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth++; depth > maxCoverDepth {
				return nil, errCoverDepth
			}
			if t.Name.Local == "binary" && attr(t, "id") == id {
				return fb2BinaryData(d)
			}
		case xml.EndElement:
			depth--
		}
	}
}

// fb2BinaryData декодирует содержимое текущего <binary>.
func fb2BinaryData(d *xml.Decoder) ([]byte, error) {
	var data []byte
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return nil, errCoverDepth
		case xml.CharData:
			data = appendBase64(data, t)
			if base64.RawStdEncoding.DecodedLen(len(data)) > MaxCover {
				return nil, fmt.Errorf("cover: %w", ErrTooLarge)
			}
		case xml.EndElement:
			if len(data) == 0 {
				return nil, nil
			}
			out := make([]byte, base64.RawStdEncoding.DecodedLen(len(data)))
			n, err := base64.RawStdEncoding.Decode(out, data)
			if err != nil {
				return nil, fmt.Errorf("cover: %w", err)
			}
			return out[:n], nil
		}
	}
}

// appendBase64 оставляет только символы алфавита: StdEncoding пропускает лишь \r и
// \n, а в FB2 бывают пробелы, табуляция и отсутствующее или лишнее дополнение.
func appendBase64(dst, src []byte) []byte {
	for _, c := range src {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' {
			dst = append(dst, c)
		}
	}
	return dst
}

type opfItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	Properties string `xml:"properties,attr"`
}

type opfPackage struct {
	Metas []struct {
		Name    string `xml:"name,attr"`
		Content string `xml:"content,attr"`
	} `xml:"metadata>meta"`
	Items []opfItem `xml:"manifest>item"`
	Spine []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

func (p *opfPackage) item(id string) *opfItem {
	i := slices.IndexFunc(p.Items, func(it opfItem) bool { return it.ID == id })
	if i < 0 {
		return nil
	}
	return &p.Items[i]
}

// candidates — href кандидатов из манифеста: cover-image, затем meta name="cover".
func (p *opfPackage) candidates() []string {
	var out []string
	for _, it := range p.Items {
		if slices.Contains(strings.Fields(it.Properties), "cover-image") {
			out = append(out, it.Href)
		}
	}
	for _, m := range p.Metas {
		if m.Name != "cover" {
			continue
		}
		if it := p.item(m.Content); it != nil {
			out = append(out, it.Href)
		}
	}
	return out
}

// epubCover ищет обложку в том же порядке, что crengine в KOReader; плюс разбор
// токенов properties, минус ветка alt="cover" на первой странице.
func epubCover(z *zip.Reader) ([]byte, error) {
	name, err := opfPath(z)
	if err != nil {
		return nil, err
	}
	data, err := readEntry(z, name, maxRead)
	if err != nil {
		return nil, err
	}
	var opf opfPackage
	if err := newXMLDecoder(bytes.NewReader(data)).Decode(&opf); err != nil {
		return nil, fmt.Errorf("opf: %w", err)
	}
	for _, href := range opf.candidates() {
		img, err := candidateImage(z, entryPath(name, href))
		if img != nil || err != nil {
			return img, err
		}
	}
	if len(opf.Spine) < 2 {
		return nil, nil
	}
	first := opf.item(opf.Spine[0].IDRef)
	if first == nil {
		return nil, nil
	}
	doc := entryPath(name, first.Href)
	page, err := readEntry(z, doc, maxCoverPage)
	if err != nil {
		return nil, readFailure(err)
	}
	src := soleImage(page)
	if src == "" {
		return nil, nil
	}
	return candidateImage(z, entryPath(doc, src))
}

// entryPath разрешает href относительно документа base внутри архива.
func entryPath(base, href string) string {
	href, _, _ = strings.Cut(href, "#")
	if s, err := url.PathUnescape(href); err == nil {
		href = s
	}
	return path.Join(path.Dir(base), href)
}

// candidateImage возвращает запись, если это изображение. Неподходящий кандидат —
// nil и nil: поиск идёт дальше.
func candidateImage(z *zip.Reader, name string) ([]byte, error) {
	b, err := readEntry(z, name, MaxCover)
	if err != nil {
		return nil, readFailure(err)
	}
	if !isImage(b) {
		return nil, nil
	}
	return b, nil
}

// readFailure оставляет только сбой чтения файла; испорченная запись — не ошибка.
func readFailure(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return err
	}
	return nil
}

// isImage распознаёт изображение по заголовку. В image/jpeg бывали паники на
// испорченных данных, а паника в горутине сканера уронила бы весь сервер.
func isImage(b []byte) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	_, _, err := image.DecodeConfig(bytes.NewReader(b))
	return err == nil
}

// soleImage возвращает ссылку на единственное изображение документа без текста
// в body или "".
func soleImage(doc []byte) string {
	d := newXMLDecoder(bytes.NewReader(doc))
	var (
		src       string
		count     int
		body, svg int
	)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "body":
				body++
			case "svg":
				svg++
			case "img":
				if body > 0 {
					src = attr(t, "src")
					count++
				}
			case "image":
				if body > 0 && svg > 0 {
					src = attr(t, "href")
					count++
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "body":
				body--
			case "svg":
				svg--
			}
		case xml.CharData:
			if body > 0 && len(strings.TrimSpace(string(t))) > 0 {
				return ""
			}
		}
	}
	if count != 1 {
		return ""
	}
	return src
}
