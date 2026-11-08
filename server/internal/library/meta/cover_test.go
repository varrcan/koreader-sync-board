package meta

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"strings"
	"syscall"
	"testing"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fb2Doc(description, tail string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink" ` +
		`xmlns:xlink="http://www.w3.org/1999/xlink"><description>` + description + `</description>` +
		`<body><section><p>Текст</p></section></body>` + tail + `</FictionBook>`
}

func coverOf(t *testing.T, data []byte, format string) []byte {
	t.Helper()
	got, err := Cover(bytes.NewReader(data), int64(len(data)), format)
	if err != nil {
		t.Fatalf("Cover: %v", err)
	}
	return got
}

func TestFB2Cover(t *testing.T) {
	img := testJPEG(t, 10, 15)
	enc := base64.StdEncoding.EncodeToString(img)
	binary := `<binary id="c.jpg" content-type="image/jpeg">` + enc + `</binary>`
	other := `<binary id="other.jpg" content-type="image/jpeg">QUJD</binary>`
	info := func(tag, href string) string {
		return `<` + tag + `><coverpage><image ` + href + `/></coverpage></` + tag + `>`
	}

	wrapped := strings.TrimRight(enc, "=")
	var broken strings.Builder
	for i := 0; i < len(wrapped); i += 40 {
		broken.WriteString(wrapped[i:min(i+40, len(wrapped))])
		broken.WriteString(" \r\n\t")
	}

	for name, doc := range map[string]string{
		"l prefix":         fb2Doc(info("title-info", `l:href="#c.jpg"`), other+binary),
		"xlink prefix":     fb2Doc(info("title-info", `xlink:href="#c.jpg"`), binary),
		"undeclared":       fb2Doc(info("title-info", `zz:href="#c.jpg"`), binary),
		"src-title-info":   fb2Doc(`<title-info/>`+info("src-title-info", `l:href="#c.jpg"`), binary),
		"title-info first": fb2Doc(info("src-title-info", `l:href="#other.jpg"`)+info("title-info", `l:href="#c.jpg"`), other+binary),
		"wrong type":       fb2Doc(info("title-info", `l:href="#c.jpg"`), `<binary id="c.jpg" content-type="image/png">`+enc+`</binary>`),
		"broken base64":    fb2Doc(info("title-info", `l:href="#c.jpg"`), `<binary id="c.jpg">`+broken.String()+`</binary>`),
	} {
		if got := coverOf(t, []byte(doc), "fb2"); !bytes.Equal(got, img) {
			t.Errorf("%s: got %d bytes, want %d", name, len(got), len(img))
		}
	}

	for name, doc := range map[string]string{
		"no reference":    fb2Doc(`<title-info><coverpage><image/></coverpage></title-info>`, binary),
		"external href":   fb2Doc(info("title-info", `l:href="c.jpg"`), binary),
		"dangling":        fb2Doc(info("title-info", `l:href="#x"`), binary),
		"image elsewhere": fb2Doc(`<title-info><annotation><image l:href="#c.jpg"/></annotation></title-info>`, binary),
	} {
		if got := coverOf(t, []byte(doc), "fb2"); got != nil {
			t.Errorf("%s: got %d bytes, want none", name, len(got))
		}
	}
}

func TestFB2CoverWindows1251(t *testing.T) {
	img := testJPEG(t, 10, 15)
	doc := `<?xml version="1.0" encoding="windows-1251"?><FictionBook xmlns:l="http://www.w3.org/1999/xlink"><description><title-info>` +
		"<book-title>\xcf\xe8\xea\xed\xe8\xea</book-title>" +
		`<coverpage><image l:href="#c"/></coverpage></title-info></description>` +
		`<binary id="c">` + base64.StdEncoding.EncodeToString(img) + `</binary></FictionBook>`
	if got := coverOf(t, []byte(doc), "fb2"); !bytes.Equal(got, img) {
		t.Errorf("got %d bytes, want %d", len(got), len(img))
	}
}

func TestFB2CoverStopsAtDescriptionEnd(t *testing.T) {
	head := `<?xml version="1.0" encoding="UTF-8"?><FictionBook><description><title-info>` +
		`<book-title>T</book-title></title-info></description>`
	got, err := fb2Cover(&failAfter{t: t, head: head}, maxCoverRead)
	if err != nil || got != nil {
		t.Fatalf("got %d bytes, %v", len(got), err)
	}
}

func TestFB2CoverZip(t *testing.T) {
	img := testJPEG(t, 10, 15)
	doc := fb2Doc(`<title-info><coverpage><image l:href="#c"/></coverpage></title-info>`,
		`<binary id="c">`+base64.StdEncoding.EncodeToString(img)+`</binary>`)
	if got := coverOf(t, zipOf(t, map[string][]byte{"readme.txt": []byte("x"), "book.fb2": []byte(doc)}), "fb2.zip"); !bytes.Equal(got, img) {
		t.Errorf("got %d bytes, want %d", len(got), len(img))
	}
	data := zipOf(t, map[string][]byte{"readme.txt": []byte("x")})
	if _, err := Cover(bytes.NewReader(data), int64(len(data)), "fb2.zip"); err == nil {
		t.Error("archive without .fb2: no error")
	}
}

func TestFB2CoverLimits(t *testing.T) {
	doc := fb2Doc(`<title-info><coverpage><image l:href="#c"/></coverpage></title-info>`,
		`<binary id="c">`+strings.Repeat("A", 8192)+`</binary>`)
	if _, err := fb2Cover(strings.NewReader(doc), 4096); !errors.Is(err, ErrTooLarge) {
		t.Errorf("read limit: %v", err)
	}
	doc = fb2Doc(`<title-info><coverpage><image l:href="#c"/></coverpage></title-info>`,
		`<binary id="c">`+strings.Repeat("A", MaxCover/3*4+8)+`</binary>`)
	if _, err := fb2Cover(strings.NewReader(doc), maxCoverRead); !errors.Is(err, ErrTooLarge) {
		t.Errorf("image limit: %v", err)
	}
}

func TestFB2CoverDepthLimit(t *testing.T) {
	img := testJPEG(t, 10, 15)
	cover := `<title-info><coverpage><image l:href="#c"/></coverpage></title-info>`
	binary := `<binary id="c">` + base64.StdEncoding.EncodeToString(img) + `</binary>`
	nested := func(n int, inner string) string {
		return strings.Repeat("<a>", n) + inner + strings.Repeat("</a>", n)
	}

	got, err := fb2Cover(strings.NewReader(fb2Doc(cover, nested(100, "")+binary)), maxCoverRead)
	if err != nil || !bytes.Equal(got, img) {
		t.Errorf("ordinary nesting: %d bytes, %v", len(got), err)
	}
	for name, doc := range map[string]string{
		"before description end": fb2Doc(`<title-info>`+nested(maxCoverDepth, "")+`</title-info>`, binary),
		"after description":      fb2Doc(cover, nested(maxCoverDepth+1, "")+binary),
		"inside binary":          fb2Doc(cover, `<binary id="c"><a>QUJD</a></binary>`),
	} {
		if _, err := fb2Cover(strings.NewReader(doc), maxCoverRead); !errors.Is(err, errCoverDepth) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// epubOf собирает EPUB с OPF в OEBPS/content.opf и дополнительными записями.
func epubOf(t *testing.T, opf string, files map[string][]byte) []byte {
	t.Helper()
	all := map[string][]byte{
		"META-INF/container.xml": []byte(`<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`),
		"OEBPS/content.opf":      []byte(opf),
	}
	for name, data := range files {
		all[name] = data
	}
	return zipOf(t, all)
}

func opfDoc(metadata, manifest, spine string) string {
	return `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf"><metadata>` + metadata +
		`</metadata><manifest>` + manifest + `</manifest><spine>` + spine + `</spine></package>`
}

func xhtml(body string) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?><!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml" ` +
		`xmlns:xlink="http://www.w3.org/1999/xlink"><head><title>Обложка</title></head><body>` + body + `</body></html>`)
}

func TestEPUBCoverManifest(t *testing.T) {
	cover, other := testJPEG(t, 10, 15), testJPEG(t, 20, 30)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	item := func(id, href, extra string) string {
		return `<item id="` + id + `" href="` + href + `" media-type="image/jpeg" ` + extra + `/>`
	}

	tests := []struct {
		name  string
		opf   string
		files map[string][]byte
		want  []byte
	}{
		{"cover-image among tokens",
			opfDoc(``, item("a", "a.jpg", ``)+item("b", "b.jpg", `properties="nav cover-image"`), ``),
			map[string][]byte{"OEBPS/a.jpg": other, "OEBPS/b.jpg": cover}, cover},
		{"substring is not a token",
			opfDoc(``, item("a", "a.jpg", `properties="not-cover-image"`), ``),
			map[string][]byte{"OEBPS/a.jpg": other}, nil},
		{"meta cover with percent-encoded href",
			opfDoc(`<meta name="cover" content="img1"/>`, item("img1", "images/%D0%BE.jpg", ``), ``),
			map[string][]byte{"OEBPS/images/о.jpg": cover}, cover},
		{"href with a parent directory",
			opfDoc(`<meta name="cover" content="i"/>`, item("i", "../art/c.jpg", ``), ``),
			map[string][]byte{"art/c.jpg": cover}, cover},
		{"cover-image wins over meta",
			opfDoc(`<meta name="cover" content="a"/>`, item("a", "a.jpg", ``)+item("b", "b.jpg", `properties="cover-image"`), ``),
			map[string][]byte{"OEBPS/a.jpg": other, "OEBPS/b.jpg": cover}, cover},
		{"svg yields to jpeg",
			opfDoc(`<meta name="cover" content="j"/>`, item("s", "s.svg", `properties="cover-image"`)+item("j", "j.jpg", ``), ``),
			map[string][]byte{"OEBPS/s.svg": svg, "OEBPS/j.jpg": cover}, cover},
		{"missing entry yields to meta",
			opfDoc(`<meta name="cover" content="j"/>`, item("s", "gone.jpg", `properties="cover-image"`)+item("j", "j.jpg", ``), ``),
			map[string][]byte{"OEBPS/j.jpg": cover}, cover},
		{"meta points nowhere",
			opfDoc(`<meta name="cover" content="x"/>`, item("j", "j.jpg", ``), ``),
			map[string][]byte{"OEBPS/j.jpg": cover}, nil},
	}
	for _, tt := range tests {
		if got := coverOf(t, epubOf(t, tt.opf, tt.files), "epub"); !bytes.Equal(got, tt.want) {
			t.Errorf("%s: got %d bytes, want %d", tt.name, len(got), len(tt.want))
		}
	}
}

func TestEPUBCoverSpine(t *testing.T) {
	cover := testJPEG(t, 10, 15)
	manifest := `<item id="p1" href="text/title.xhtml" media-type="application/xhtml+xml"/>` +
		`<item id="p2" href="text/ch1.xhtml" media-type="application/xhtml+xml"/>`
	two := `<itemref idref="p1"/><itemref idref="p2"/>`
	opf := opfDoc(``, manifest, two)
	files := func(body []byte) map[string][]byte {
		return map[string][]byte{"OEBPS/text/title.xhtml": body, "OEBPS/images/cover.jpg": cover, "OEBPS/images/b.jpg": cover}
	}

	tests := []struct {
		name string
		opf  string
		body []byte
		want []byte
	}{
		{"sole img", opf, xhtml(`<div><img src="../images/cover.jpg" alt=""/></div>`), cover},
		{"nbsp only", opf, xhtml(`<p>&nbsp;</p><img src="../images/cover.jpg"/>`), cover},
		{"image in svg", opf, xhtml(`<svg xmlns="http://www.w3.org/2000/svg"><image xlink:href="../images/cover.jpg"/></svg>`), cover},
		{"text", opf, xhtml(`<img src="../images/cover.jpg"/><p>Глава первая</p>`), nil},
		{"two images", opf, xhtml(`<img src="../images/cover.jpg"/><img src="../images/b.jpg"/>`), nil},
		{"image outside svg", opf, xhtml(`<image href="../images/cover.jpg"/>`), nil},
		{"single document in spine", opfDoc(``, manifest, `<itemref idref="p1"/>`), xhtml(`<img src="../images/cover.jpg"/>`), nil},
		{"too large", opf, xhtml(`<img src="../images/cover.jpg"/><!--` + strings.Repeat("x", 5000) + `-->`), nil},
		{"not an image", opf, xhtml(`<img src="ch1.xhtml"/>`), nil},
	}
	for _, tt := range tests {
		if got := coverOf(t, epubOf(t, tt.opf, files(tt.body)), "epub"); !bytes.Equal(got, tt.want) {
			t.Errorf("%s: got %d bytes, want %d", tt.name, len(got), len(tt.want))
		}
	}
}

func TestEPUBCoverDamaged(t *testing.T) {
	data := zipOf(t, map[string][]byte{"a.txt": []byte("x")})
	if _, err := Cover(bytes.NewReader(data), int64(len(data)), "epub"); err == nil {
		t.Error("no container.xml: no error")
	}
	if _, err := Cover(strings.NewReader("garbage"), 7, "epub"); err == nil {
		t.Error("not a zip: no error")
	}
	var pe *fs.PathError
	if _, err := Cover(strings.NewReader("garbage"), 7, "epub"); errors.As(err, &pe) {
		t.Errorf("format error looks like a read error: %v", err)
	}
}

func TestCoverOtherFormats(t *testing.T) {
	got, err := Cover(failingReaderAt{}, 100, "pdf")
	if err != nil || got != nil {
		t.Fatalf("got %d bytes, %v", len(got), err)
	}
}

// failingReaderAt ведёт себя как os.File на отвалившемся NFS.
type failingReaderAt struct{}

func (failingReaderAt) ReadAt([]byte, int64) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: "book", Err: syscall.EIO}
}

// Ошибка чтения должна дойти до сканера как PathError: только она оставляет файл
// непроверенным.
func TestCoverReadErrorIsPathError(t *testing.T) {
	var pe *fs.PathError
	for _, format := range []string{"fb2", "fb2.zip", "epub"} {
		if _, err := Cover(failingReaderAt{}, 4096, format); !errors.As(err, &pe) {
			t.Errorf("%s: %v", format, err)
		}
	}

	// Каталог и OPF читаются, сбой — на теле сжатой картинки: flate не должен прятать
	// ошибку источника.
	cover := testJPEG(t, 10, 15)
	data := epubOf(t, opfDoc(`<meta name="cover" content="j"/>`,
		`<item id="j" href="j.jpg" media-type="image/jpeg"/>`, ``), map[string][]byte{"OEBPS/j.jpg": cover})
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var offset int64
	for _, f := range z.File {
		if f.Name != "OEBPS/j.jpg" {
			continue
		}
		if offset, err = f.DataOffset(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Cover(windowFail{data: data, start: offset, end: offset + 1}, int64(len(data)), "epub"); !errors.As(err, &pe) {
		t.Errorf("failure inside an image: %v", err)
	}
}

// windowFail даёт ошибку чтения при обращении к диапазону [start, end).
type windowFail struct {
	data       []byte
	start, end int64
}

func (w windowFail) ReadAt(p []byte, off int64) (int, error) {
	if off < w.end && off+int64(len(p)) > w.start {
		return 0, &fs.PathError{Op: "read", Path: "book", Err: syscall.EIO}
	}
	n := copy(p, w.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
