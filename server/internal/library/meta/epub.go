package meta

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// EPUB разбирает OPF, найденный через META-INF/container.xml. zip.OpenReader
// читает только центральный каталог, поэтому из архива читаются лишь
// container.xml и OPF.
func EPUB(name string) (Book, error) {
	z, err := zip.OpenReader(name)
	if err != nil {
		return Book{}, err
	}
	defer z.Close()
	return epubArchive(&z.Reader)
}

func epubArchive(z *zip.Reader) (Book, error) {
	opf, err := opfPath(z)
	if err != nil {
		return Book{}, err
	}
	data, err := readEntry(z, opf, maxRead)
	if err != nil {
		return Book{}, err
	}
	return parseOPF(data)
}

// opfPath — путь OPF в архиве из META-INF/container.xml.
func opfPath(z *zip.Reader) (string, error) {
	data, err := readEntry(z, "META-INF/container.xml", maxRead)
	if err != nil {
		return "", err
	}
	var c struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("container.xml: %w", err)
	}
	if len(c.Rootfiles) == 0 || c.Rootfiles[0].FullPath == "" {
		return "", errors.New("container.xml: no rootfile")
	}
	return strings.TrimPrefix(path.Clean(c.Rootfiles[0].FullPath), "/"), nil
}

func readEntry(z *zip.Reader, name string, maxBytes int64) ([]byte, error) {
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		if f.UncompressedSize64 > uint64(maxBytes) {
			return nil, fmt.Errorf("%s: %w", name, ErrTooLarge)
		}
		r, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		defer r.Close()
		b, err := readAll(r, maxBytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("%s not found", name)
}

type opfCreator struct {
	id, role, name string
}

// opfMeta — <meta> EPUB 2 (name/content) или EPUB 3 (property/refines/текст).
type opfMeta struct {
	name, content, property, refines, id, text string
}

// parseOPF извлекает метаданные из <metadata>. Сравниваются только локальные
// имена: префиксы dc/opf в реальных файлах объявлены по-разному.
func parseOPF(data []byte) (Book, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.Entity = xml.HTMLEntity

	var (
		b        Book
		creators []opfCreator
		metas    []opfMeta
		depth    int // глубина внутри <metadata>; 0 — снаружи
		text     []byte
		cur      *opfCreator
		curMeta  *opfMeta
		seenMeta bool
		scheme   string
		isbn     string // явно объявленный ISBN
		bare     string // значение без схемы, случайно похожее на ISBN, — запасной вариант
		desc     *strings.Builder
	)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Book{}, fmt.Errorf("opf: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if t.Name.Local == "metadata" {
					depth, seenMeta = 1, true
				}
				continue
			}
			depth++
			text = text[:0]
			if depth != 2 {
				// HTML аннотации бывает разметкой, а не экранированным текстом:
				// теги остаются в тексте, их разберёт PlainText.
				if desc != nil {
					desc.WriteString("<" + t.Name.Local + ">")
				}
				continue
			}
			switch t.Name.Local {
			case "identifier":
				scheme = attr(t, "scheme")
			case "description":
				desc = &strings.Builder{}
			case "creator":
				cur = &opfCreator{id: attr(t, "id"), role: attr(t, "role")}
			case "meta":
				curMeta = &opfMeta{name: attr(t, "name"), content: attr(t, "content"),
					property: attr(t, "property"), refines: attr(t, "refines"), id: attr(t, "id")}
			}
		case xml.CharData:
			if depth > 0 {
				text = append(text, t...)
			}
			if desc != nil {
				desc.Write(t)
			}
		case xml.EndElement:
			if depth == 0 {
				continue
			}
			if depth == 1 {
				depth = 0
				continue
			}
			if depth > 2 && desc != nil {
				desc.WriteString("</" + t.Name.Local + ">")
			}
			if depth == 2 {
				v := clean(string(text))
				switch t.Name.Local {
				case "identifier":
					switch n := NormalizeISBN(v); {
					case n == "":
					case strings.EqualFold(scheme, "isbn") || strings.HasPrefix(strings.ToLower(v), "urn:isbn:"):
						if isbn == "" {
							isbn = n
						}
					case scheme == "" && bare == "":
						bare = n
					}
				case "description":
					if desc != nil && b.Description == "" {
						b.Description = PlainText(desc.String())
					}
					desc = nil
				case "title":
					if b.Title == "" {
						b.Title = v
					}
				case "language":
					if b.Language == "" {
						b.Language = v
					}
				case "creator":
					if cur != nil {
						cur.name = v
						creators = append(creators, *cur)
					}
					cur = nil
				case "meta":
					if curMeta != nil {
						curMeta.text = v
						metas = append(metas, *curMeta)
					}
					curMeta = nil
				}
			}
			depth--
		}
	}
	if !seenMeta {
		return Book{}, errors.New("opf: no <metadata>")
	}

	roles := map[string]string{}
	for _, m := range metas {
		if m.property == "role" && strings.HasPrefix(m.refines, "#") {
			roles[m.refines[1:]] = m.text
		}
	}
	for _, c := range creators {
		role := c.role
		if r, ok := roles[c.id]; ok && c.id != "" {
			role = r
		}
		if (role == "" || role == "aut") && c.name != "" {
			b.Authors = append(b.Authors, c.name)
		}
	}
	b.Series, b.SeriesIndex = opfSeries(metas)
	b.ISBN = isbn
	if b.ISBN == "" {
		b.ISBN = bare
	}
	return b, nil
}

// opfSeries — серия calibre, иначе первая belongs-to-collection EPUB 3.
func opfSeries(metas []opfMeta) (string, *float64) {
	var series, index string
	for _, m := range metas {
		switch m.name {
		case "calibre:series":
			if series == "" {
				series = clean(m.content)
			}
		case "calibre:series_index":
			if index == "" {
				index = m.content
			}
		}
	}
	if series != "" {
		return series, parseIndex(index)
	}
	for _, m := range metas {
		if m.property != "belongs-to-collection" || m.text == "" {
			continue
		}
		for _, r := range metas {
			if r.property == "group-position" && m.id != "" && r.refines == "#"+m.id {
				return m.text, parseIndex(r.text)
			}
		}
		return m.text, nil
	}
	return "", nil
}

func attr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
