package library

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOracle сканирует корень книг с настоящей читалки и ищет по partial MD5 из
// .sdr свой файл в индексе. Переменные — те же, что у TestOracle в hashing.
func TestOracle(t *testing.T) {
	sdrRoot := os.Getenv("KSB_ORACLE_SDR_ROOT")
	if sdrRoot == "" {
		t.Skip("KSB_ORACLE_SDR_ROOT not set")
	}
	mode := os.Getenv("KSB_ORACLE_MODE")
	if mode == "" {
		mode = "doc"
	}
	prefix := os.Getenv("KSB_ORACLE_DEVICE_PREFIX")
	booksRoot := os.Getenv("KSB_ORACLE_BOOKS_ROOT")
	if mode == "doc" {
		booksRoot = sdrRoot
	}
	if booksRoot == "" {
		t.Fatalf("KSB_ORACLE_BOOKS_ROOT is required in %s mode", mode)
	}
	booksRoot, err := filepath.Abs(booksRoot)
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)
	r := e.addRoot(booksRoot)
	e.scan()
	if got := e.root(r.ID); got.Error != nil {
		t.Logf("scan error: %s", *got.Error)
	}

	metadataName := regexp.MustCompile(`^metadata\.(.+)\.lua$`)
	var matched, mismatched, missing int
	err = filepath.WalkDir(sdrRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !metadataName.MatchString(d.Name()) {
			return err
		}
		sdr := filepath.Dir(path)
		if !strings.HasSuffix(sdr, ".sdr") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		want := luaString(content, "partial_md5_checksum")
		if want == "" {
			return nil
		}
		ext := metadataName.FindStringSubmatch(d.Name())[1]
		var devicePath string
		switch mode {
		case "doc":
			rel, _ := filepath.Rel(sdrRoot, strings.TrimSuffix(sdr, ".sdr")+"."+ext)
			devicePath = prefix + "/" + filepath.ToSlash(rel)
		case "dir":
			rel, _ := filepath.Rel(sdrRoot, strings.TrimSuffix(sdr, ".sdr")+"."+ext)
			devicePath = "/" + filepath.ToSlash(rel)
		case "hash":
			devicePath = luaString(content, "doc_path")
		default:
			t.Fatalf("unknown KSB_ORACLE_MODE %q", mode)
		}
		rel, ok := strings.CutPrefix(devicePath, prefix+"/")
		if !ok {
			missing++
			return nil
		}
		var got string
		err = e.db.Read.QueryRowContext(e.ctx, `SELECT f.path FROM library_file_hashes h
			JOIN library_files f ON f.id = h.file_id WHERE h.hash = ? ORDER BY f.id LIMIT 1`, want).Scan(&got)
		switch {
		case err == nil && got == rel:
			matched++
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			t.Fatal(err)
		case e.count(`SELECT count(*) FROM library_files WHERE path = ?`, rel) == 0:
			t.Logf("NO BOOK    %s", rel)
			missing++
		default:
			// Хеш в .sdr считается один раз и устаревает, если файл заменили.
			t.Logf("MISMATCH   %s: .sdr %s, index %q", rel, want, got)
			mismatched++
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

func luaString(content []byte, name string) string {
	re := regexp.MustCompile(`\["` + regexp.QuoteMeta(name) + `"\]\s*=\s*"((?:[^"\\]|\\.)*)"`)
	m := re.FindSubmatch(content)
	if m == nil {
		return ""
	}
	return strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\t`, "\t", `\r`, "\r").Replace(string(m[1]))
}
