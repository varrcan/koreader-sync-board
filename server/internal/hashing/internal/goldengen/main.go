// Команда goldengen пересчитывает testdata/golden.json: создаёт синтетические файлы,
// прогоняет по ним testdata/oracle.lua в LuaJIT и хеширует вывод через md5sum.
//
//	go run ./internal/hashing/internal/goldengen -luajit luajit
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/varrcan/koreader-sync-board/server/internal/hashing/internal/synth"
)

func main() {
	luajit := flag.String("luajit", "luajit", "команда запуска LuaJIT (можно с аргументами)")
	oracle := flag.String("oracle", "internal/hashing/testdata/oracle.lua", "путь к oracle.lua")
	out := flag.String("out", "internal/hashing/testdata/golden.json", "куда записать эталон")
	flag.Parse()

	oraclePath, err := filepath.Abs(*oracle)
	if err != nil {
		log.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "ksb-golden-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	cases := synth.Cases()
	for i := range cases {
		c := &cases[i]
		path := filepath.Join(dir, c.Name)
		if err := synth.Write(path, *c); err != nil {
			log.Fatal(err)
		}
		c.MD5, err = oracleMD5(*luajit, oraclePath, path)
		if err != nil {
			log.Fatalf("%s: %v", c.Name, err)
		}
		os.Remove(path)
		fmt.Printf("%-24s %s\n", c.Name, c.MD5)
	}

	data, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
}

// oracleMD5 выполняет `luajit oracle.lua <file> | md5sum`.
func oracleMD5(luajit, oracle, file string) (string, error) {
	args := append(strings.Fields(luajit), oracle, file)
	lua := exec.Command(args[0], args[1:]...)
	lua.Stderr = os.Stderr
	md5sum := exec.Command("md5sum")
	pipe, err := lua.StdoutPipe()
	if err != nil {
		return "", err
	}
	md5sum.Stdin = pipe
	var out bytes.Buffer
	md5sum.Stdout = &out
	if err := md5sum.Start(); err != nil {
		return "", err
	}
	if err := lua.Run(); err != nil {
		return "", fmt.Errorf("luajit: %w", err)
	}
	if err := md5sum.Wait(); err != nil {
		return "", fmt.Errorf("md5sum: %w", err)
	}
	sum, _, _ := strings.Cut(out.String(), " ")
	if len(sum) != 32 {
		return "", fmt.Errorf("unexpected md5sum output %q", out.String())
	}
	return sum, nil
}
