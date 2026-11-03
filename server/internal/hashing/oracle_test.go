package hashing

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOracle сверяет PartialMD5 с partial_md5_checksum из .sdr, скопированных
// с настоящей читалки. Личные книги в репозиторий не попадают, поэтому тест
// запускается только с переменными окружения:
//
//	KSB_ORACLE_SDR_ROOT       где искать *.sdr (в режиме doc книги лежат рядом)
//	KSB_ORACLE_MODE           doc (по умолчанию), dir или hash — document_metadata_folder
//	KSB_ORACLE_DEVICE_PREFIX  корень книг на устройстве, например /mnt/ext1 (dir, hash)
//	KSB_ORACLE_BOOKS_ROOT     чем заменить этот префикс локально (dir, hash)
//
// В режиме dir путь .sdr внутри SDR_ROOT повторяет абсолютный путь книги на
// устройстве; в режиме hash путь берётся из doc_path в metadata.*.lua.
func TestOracle(t *testing.T) {
	root := os.Getenv("KSB_ORACLE_SDR_ROOT")
	if root == "" {
		t.Skip("KSB_ORACLE_SDR_ROOT not set")
	}
	mode := os.Getenv("KSB_ORACLE_MODE")
	if mode == "" {
		mode = "doc"
	}
	prefix := os.Getenv("KSB_ORACLE_DEVICE_PREFIX")
	booksRoot := os.Getenv("KSB_ORACLE_BOOKS_ROOT")
	if mode != "doc" && booksRoot == "" {
		t.Fatalf("KSB_ORACLE_BOOKS_ROOT is required in %s mode", mode)
	}

	toLocal := func(devicePath string) (string, bool) {
		rel, ok := strings.CutPrefix(devicePath, prefix)
		if !ok {
			return "", false
		}
		return filepath.Join(booksRoot, filepath.FromSlash(rel)), true
	}

	var matched, mismatched, missing int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !metadataName.MatchString(d.Name()) {
			return nil
		}
		sdr := filepath.Dir(path)
		if !strings.HasSuffix(sdr, ".sdr") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		want := luaField(content, "partial_md5_checksum")
		if want == "" {
			t.Logf("NO CHECKSUM %s", path)
			return nil
		}

		// metadata.<ext>.lua: KOReader отрезает у книги только последнее расширение.
		ext := metadataName.FindStringSubmatch(d.Name())[1]
		var book string
		switch mode {
		case "doc":
			book = strings.TrimSuffix(sdr, ".sdr") + "." + ext
		case "dir":
			rel, _ := filepath.Rel(root, strings.TrimSuffix(sdr, ".sdr")+"."+ext)
			var ok bool
			if book, ok = toLocal("/" + filepath.ToSlash(rel)); !ok {
				t.Logf("OUTSIDE PREFIX /%s", filepath.ToSlash(rel))
				missing++
				return nil
			}
		case "hash":
			docPath := luaField(content, "doc_path")
			var ok bool
			if book, ok = toLocal(docPath); !ok {
				t.Logf("OUTSIDE PREFIX %s", docPath)
				missing++
				return nil
			}
		default:
			t.Fatalf("unknown KSB_ORACLE_MODE %q", mode)
		}

		got, err := PartialMD5(book)
		switch {
		case os.IsNotExist(err):
			t.Logf("NO BOOK    %s", book)
			missing++
		case err != nil:
			t.Errorf("ERROR      %s: %v", book, err)
		case got != want:
			// Хеш в .sdr считается один раз; если файл заменили по тому же пути,
			// он устаревает. Это не ошибка хеширования, но в отчёте должно быть видно.
			t.Logf("MISMATCH   %s: computed %s, .sdr %s", book, got, want)
			mismatched++
		default:
			matched++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("oracle: %d matched, %d mismatched (stale .sdr?), %d without book", matched, mismatched, missing)
	if matched == 0 {
		t.Fatal("no book matched: check oracle settings")
	}
}

var metadataName = regexp.MustCompile(`^metadata\.(.+)\.lua$`)

func luaField(content []byte, name string) string {
	re := regexp.MustCompile(`\["` + regexp.QuoteMeta(name) + `"\]\s*=\s*"((?:[^"\\]|\\.)*)"`)
	m := re.FindSubmatch(content)
	if m == nil {
		return ""
	}
	return unescapeLua(string(m[1]))
}

// unescapeLua раскрывает экранирование строк, которое использует dump() KOReader.
func unescapeLua(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\t`, "\t", `\r`, "\r")
	return r.Replace(s)
}
