package opds

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/library"
)

const (
	navigationType  = "application/atom+xml;profile=opds-catalog;kind=navigation"
	acquisitionType = "application/atom+xml;profile=opds-catalog;kind=acquisition"
	openSearchType  = "application/opensearchdescription+xml"
	// searchTemplateType — тип шаблонной ссылки поиска и Url в описании OpenSearch.
	searchTemplateType = "application/atom+xml"

	// acquisitionRel подходит шаблону acquisition_rel KOReader, а CrossPoint ставит
	// open-access выше остальных вариантов получения.
	acquisitionRel = "http://opds-spec.org/acquisition/open-access"
	imageRel       = "http://opds-spec.org/image"
	thumbnailRel   = "http://opds-spec.org/image/thumbnail"

	searchTemplate = Prefix + "/search?q={searchTerms}"
	epubType       = "application/epub+zip"
	coverType      = "image/jpeg"
)

// formatTypes — тип ссылки по формату. KOReader выбирает расширение файла по типу
// (DocumentRegistry:mimeToExt), поэтому здесь типы, для которых нужное расширение
// зарегистрировано первым: у application/zip это был бы не fb2.zip.
var formatTypes = map[string]string{
	"epub":    epubType,
	"fb2":     "application/x-fictionbook+xml",
	"fb2.zip": "application/fb2+zip",
	"pdf":     "application/pdf",
	"djvu":    "image/vnd.djvu",
	"mobi":    "application/x-mobipocket-ebook",
	"azw":     "application/vnd.amazon.mobi8-ebook",
	"cbz":     "application/vnd.comicbook+zip",
}

func formatType(format string) string {
	if t, ok := formatTypes[format]; ok {
		return t
	}
	return "application/octet-stream"
}

type feed struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string   `xml:"id"`
	Title   string   `xml:"title"`
	Updated string   `xml:"updated"`
	Links   []link   `xml:"link"`
	Entries []entry  `xml:"entry"`
}

// entry: описание — summary, а не content: OPDSParser:parse переписывает content
// регулярными выражениями, summary идёт обычным текстом.
type entry struct {
	Title   string  `xml:"title"`
	ID      string  `xml:"id"`
	Updated string  `xml:"updated"`
	Author  *author `xml:"author"`
	Summary string  `xml:"summary,omitempty"`
	Links   []link  `xml:"link"`
}

// author — одна строка на всех авторов: разбиение по запятой ломало бы имена вроде
// «Толкин, Дж. Р. Р.», а KOReader и CrossPoint показывают строку как есть.
type author struct {
	Name string `xml:"name"`
}

type link struct {
	Rel    string `xml:"rel,attr"`
	Href   string `xml:"href,attr"`
	Type   string `xml:"type,attr"`
	Title  string `xml:"title,attr,omitempty"`
	Length int64  `xml:"length,attr,omitempty"`
}

type openSearch struct {
	XMLName     xml.Name      `xml:"http://a9.com/-/spec/opensearch/1.1/ OpenSearchDescription"`
	ShortName   string        `xml:"ShortName"`
	Description string        `xml:"Description"`
	URL         openSearchURL `xml:"Url"`
}

type openSearchURL struct {
	Type     string `xml:"type,attr"`
	Template string `xml:"template,attr"`
}

func openSearchDocument() openSearch {
	return openSearch{ShortName: "Library", Description: "Search the library",
		URL: openSearchURL{Type: searchTemplateType, Template: searchTemplate}}
}

func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// rootFeed — корень. Описание OpenSearch стоит раньше шаблона и только здесь: иначе
// KOReader запрашивал бы его на каждой странице.
func rootFeed(now time.Time) feed {
	updated := timestamp(now)
	return feed{ID: "urn:ksb:opds:root", Title: "Library", Updated: updated,
		Links: []link{
			{Rel: "self", Href: Prefix, Type: navigationType},
			{Rel: "start", Href: Prefix, Type: navigationType},
			{Rel: "search", Href: Prefix + "/opensearch.xml", Type: openSearchType},
			{Rel: "search", Href: searchTemplate, Type: searchTemplateType},
		},
		Entries: []entry{{Title: "All books", ID: "urn:ksb:opds:books", Updated: updated,
			Links: []link{{Rel: "subsection", Href: Prefix + "/books", Type: acquisitionType}}}},
	}
}

// feedMeta — что отличает ленты книг друг от друга.
type feedMeta struct {
	id, title, path string
	query           url.Values
	before          int64
}

// href — адрес страницы ленты с курсором before (0 — первая страница).
func (m feedMeta) href(before int64) string {
	q := url.Values{}
	for k, v := range m.query {
		q[k] = v
	}
	if before > 0 {
		q.Set("before", strconv.FormatInt(before, 10))
	}
	if len(q) == 0 {
		return m.path
	}
	return m.path + "?" + q.Encode()
}

// bookFeed — лента книг. Ссылка поиска есть и в пустой ленте: KOReader показывает
// ленту, только если в ней есть записи, фасеты или search_url.
func bookFeed(m feedMeta, page library.FeedPage, now time.Time) feed {
	f := feed{ID: m.id, Title: m.title, Updated: timestamp(now),
		Links: []link{
			{Rel: "self", Href: m.href(m.before), Type: acquisitionType},
			{Rel: "start", Href: Prefix, Type: navigationType},
			{Rel: "search", Href: searchTemplate, Type: searchTemplateType},
		},
		Entries: make([]entry, 0, len(page.Items)),
	}
	if page.Next != 0 {
		f.Links = append(f.Links, link{Rel: "next", Href: m.href(page.Next), Type: acquisitionType})
	}
	for _, b := range page.Items {
		f.Entries = append(f.Entries, bookEntry(b))
	}
	return f
}

func bookEntry(b library.FeedBook) entry {
	e := entry{Title: bookTitle(b), ID: fmt.Sprintf("urn:ksb:book:%d", b.Book.ID), Updated: timestamp(b.Updated), Links: bookLinks(b)}
	if b.Book.Authors != nil && *b.Book.Authors != "" {
		e.Author = &author{Name: *b.Book.Authors}
	}
	if b.Description != nil {
		e.Summary = *b.Description
	}
	return e
}

// bookTitle — название из метаданных, иначе имя первого файла без расширения.
func bookTitle(b library.FeedBook) string {
	if b.Book.Title != nil && *b.Book.Title != "" {
		return *b.Book.Title
	}
	return b.Book.Filename[:len(b.Book.Filename)-len(b.Files[0].Format)-1]
}

// bookLinks: адреса файлов намеренно без расширения. OPDSBrowser.getFiletype берёт
// суффикс адреса, и только если для него нет провайдера — тип: в /opds/files/7/x.fb2.zip
// суффикс «zip», и книга сохранилась бы как .zip.
func bookLinks(b library.FeedBook) []link {
	links := make([]link, 0, len(b.Files)+3)
	hasEPUB := false
	var source *library.FeedFile
	for i, f := range b.Files {
		links = append(links, link{Rel: acquisitionRel, Href: fileHref(f.ID), Type: formatType(f.Format),
			Title: strings.ToUpper(f.Format), Length: f.Size})
		hasEPUB = hasEPUB || f.Format == "epub"
		if source == nil && (f.Format == "fb2" || f.Format == "fb2.zip") {
			source = &b.Files[i]
		}
	}
	// Ссылка одна: CrossPoint берёт первую подходящую EPUB-ссылку, а несколько
	// одинаковых подписей в KOReader только путают.
	if source != nil && !hasEPUB {
		links = append(links, link{Rel: acquisitionRel, Href: fileHref(source.ID) + "/epub", Type: epubType, Title: "EPUB"})
	}
	if b.Book.Cover != nil {
		href := Prefix + "/covers/" + *b.Book.Cover
		links = append(links, link{Rel: imageRel, Href: href, Type: coverType}, link{Rel: thumbnailRel, Href: href, Type: coverType})
	}
	return links
}

func fileHref(id int64) string { return fmt.Sprintf("%s/files/%d", Prefix, id) }
