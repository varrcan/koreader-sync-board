package meta

import (
	"html"
	"strings"
)

// isbnDashes — разделители, встречающиеся в ISBN: обычный дефис и его типографские родственники.
const isbnDashes = "-‐‑‒–—−"

// NormalizeISBN приводит ISBN-10 или ISBN-13 к цифрам (и X). Значение с посторонними
// символами или неверной контрольной суммой даёт пустую строку.
func NormalizeISBN(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "urn:")
	s = strings.TrimPrefix(s, "isbn")
	s = strings.TrimLeft(s, ": ")
	var out []byte
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			out = append(out, byte(r))
		case r == 'x':
			out = append(out, 'X')
		case r == ' ' || strings.ContainsRune(isbnDashes, r):
		default:
			return ""
		}
	}
	switch {
	case len(out) == 10 && validISBN10(out):
	case len(out) == 13 && validISBN13(out):
	default:
		return ""
	}
	return string(out)
}

func validISBN10(b []byte) bool {
	sum := 0
	for i, c := range b {
		var d int
		switch {
		case c == 'X' && i == 9:
			d = 10
		case c == 'X':
			return false
		default:
			d = int(c - '0')
		}
		sum += d * (10 - i)
	}
	return sum%11 == 0
}

func validISBN13(b []byte) bool {
	// EAN-13 с другим префиксом — не книга.
	if s := string(b[:3]); s != "978" && s != "979" {
		return false
	}
	sum := 0
	for i, c := range b {
		if c == 'X' {
			return false
		}
		d := int(c - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return sum%10 == 0
}

// blockTags — HTML-элементы, разделяющие абзацы.
var blockTags = map[string]bool{
	"p": true, "br": true, "div": true, "li": true, "ul": true, "ol": true, "tr": true,
	"blockquote": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

// PlainText превращает HTML в простой текст: теги убираются, сущности раскрываются,
// абзацы разделяются пустой строкой. Перенос строки разделяет абзацы, только если в
// тексте нет блочной разметки: в HTML это обычный пробел.
func PlainText(s string) string {
	var (
		cur     strings.Builder
		parts   []string
		blocks  bool
		rest    = s
		breakAt = func() {
			parts = append(parts, cur.String())
			cur.Reset()
		}
	)
	for {
		i := strings.IndexByte(rest, '<')
		if i < 0 {
			cur.WriteString(rest)
			break
		}
		cur.WriteString(rest[:i])
		rest = rest[i:]
		end := strings.IndexByte(rest, '>')
		// «a < b» — не тег.
		if end < 0 || !tagStart(rest) {
			cur.WriteByte('<')
			rest = rest[1:]
			continue
		}
		if blockTags[tagName(rest[1:end])] {
			blocks = true
			breakAt()
		}
		rest = rest[end+1:]
	}
	breakAt()

	var out []string
	for _, part := range parts {
		lines := []string{part}
		if !blocks {
			lines = strings.Split(part, "\n")
		}
		for _, line := range lines {
			if p := clean(html.UnescapeString(line)); p != "" {
				out = append(out, p)
			}
		}
	}
	return strings.Join(out, "\n\n")
}

// tagStart: после «<» идёт имя тега, закрывающий тег или комментарий.
func tagStart(s string) bool {
	if len(s) < 2 {
		return false
	}
	c := s[1]
	return c == '/' || c == '!' || c == '?' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func tagName(tag string) string {
	tag = strings.TrimLeft(tag, "/")
	if i := strings.IndexAny(tag, " \t\r\n/"); i >= 0 {
		tag = tag[:i]
	}
	return strings.ToLower(tag)
}
