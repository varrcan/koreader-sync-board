package meta

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"

	"golang.org/x/text/encoding/htmlindex"
)

// FB2 потоково разбирает <description><title-info> и прекращает чтение на
// </description>: тело книги и binary не читаются. Кодировка — из XML-декларации.
// encoding/xml читает декларацию как ASCII до CharsetReader, поэтому UTF-16 не
// поддерживается: такой файл останется без метаданных.
func FB2(r io.Reader) (Book, error) {
	d := xml.NewDecoder(limit(stripBOM(r)))
	// FB2 из сети часто с &nbsp; и мелкими огрехами разметки.
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		enc, err := htmlindex.Get(label)
		if err != nil {
			return nil, fmt.Errorf("unsupported encoding %q", label)
		}
		return enc.NewDecoder().Reader(input), nil
	}

	var (
		b       Book
		stack   []string
		text    []byte
		author  map[string]string
		authors []string
	)
	// inTitleInfo: путь <корень>/description/title-info/…; src-title-info и
	// document-info тоже содержат book-title и author.
	inTitleInfo := func() bool {
		return len(stack) >= 3 && stack[1] == "description" && stack[2] == "title-info"
	}
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return Book{}, errors.New("no <description>")
		}
		if err != nil {
			return Book{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			text = text[:0]
			if !inTitleInfo() || len(stack) != 4 {
				continue
			}
			switch t.Name.Local {
			case "author":
				author = map[string]string{}
			case "sequence":
				if b.Series == "" {
					for _, a := range t.Attr {
						switch a.Name.Local {
						case "name":
							b.Series = clean(a.Value)
						case "number":
							b.SeriesIndex = parseIndex(a.Value)
						}
					}
					if b.Series == "" {
						b.SeriesIndex = nil
					}
				}
			}
		case xml.CharData:
			text = append(text, t...)
		case xml.EndElement:
			// Нестрогий режим пропускает незакрытые теги: снимаем со стека до
			// совпадающего имени, лишний закрывающий тег игнорируем.
			i := len(stack) - 1
			for i >= 0 && stack[i] != t.Name.Local {
				i--
			}
			if i < 0 {
				continue
			}
			stack = stack[:i+1]
			if inTitleInfo() {
				switch {
				case len(stack) == 4 && t.Name.Local == "book-title" && b.Title == "":
					b.Title = clean(string(text))
				case len(stack) == 4 && t.Name.Local == "lang" && b.Language == "":
					b.Language = clean(string(text))
				case len(stack) == 4 && t.Name.Local == "author":
					if name := authorName(author); name != "" {
						authors = append(authors, name)
					}
					author = nil
				case len(stack) == 5 && stack[3] == "author" && author != nil:
					author[t.Name.Local] = clean(string(text))
				}
			}
			stack = stack[:i]
			if t.Name.Local == "description" && len(stack) == 1 {
				b.Authors = authors
				return b, nil
			}
		}
	}
}

// authorName — «Имя Отчество Фамилия», а без них — nickname.
func authorName(a map[string]string) string {
	name := clean(a["first-name"] + " " + a["middle-name"] + " " + a["last-name"])
	if name == "" {
		name = a["nickname"]
	}
	return name
}
