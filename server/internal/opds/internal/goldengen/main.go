// Команда goldengen пересчитывает testdata/golden.json: скачивает парсер OPDS из
// KOReader на закреплённом коммите и прогоняет через него testdata/feed.xml в LuaJIT.
//
//	go run ./internal/opds/internal/goldengen -luajit luajit
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// commit — коммит koreader/koreader, с которым сверяется лента.
const commit = "01ccbd790d6269301ad523e75c45f62d022a6d7b"

var sources = []string{"plugins/opds.koplugin/opdsparser.lua", "frontend/luxl.lua"}

func main() {
	luajit := flag.String("luajit", "luajit", "команда запуска LuaJIT (можно с аргументами)")
	oracle := flag.String("oracle", "internal/opds/testdata/oracle.lua", "путь к oracle.lua")
	feed := flag.String("feed", "internal/opds/testdata/feed.xml", "снимок ленты")
	out := flag.String("out", "internal/opds/testdata/golden.json", "куда записать эталон")
	flag.Parse()

	oraclePath, err := filepath.Abs(*oracle)
	if err != nil {
		log.Fatal(err)
	}
	feedPath, err := filepath.Abs(*feed)
	if err != nil {
		log.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "ksb-opds-golden-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for _, src := range sources {
		if err := download(src, filepath.Join(dir, filepath.Base(src))); err != nil {
			log.Fatal(err)
		}
	}

	args := append(strings.Fields(*luajit), oraclePath, dir, feedPath)
	cmd := exec.Command(args[0], args[1:]...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("luajit: %v", err)
	}
	if err := os.WriteFile(*out, stdout.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

func download(src, dst string) error {
	url := "https://raw.githubusercontent.com/koreader/koreader/" + commit + "/" + src
	res, err := http.Get(url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, res.Status)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
