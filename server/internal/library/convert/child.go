// Package convert превращает FB2 в EPUB через fb2cng. Сама конвертация идёт в
// дочернем процессе того же бинарника: recover не ловит ни нехватку памяти, ни
// паники чужих горутин, а запустить её может любой пользователь.
package convert

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"

	"go.uber.org/zap"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"fbc/common"
	"fbc/config"
	"fbc/content"
	"fbc/convert/epub"
	"fbc/state"

	"github.com/varrcan/koreader-sync-board/server/internal/library/meta"
)

// Копия convert/default.css из fb2cng: там он лежит в закрытой переменной пакета.
//
//go:embed default.css
var defaultCSS []byte

// Коды выхода дочернего процесса. Остальные, включая завершение сигналом, — сбой
// конвертации.
const (
	exitFailed     = 1
	exitTooLarge   = 3
	exitSourceOpen = 4
)

// outputFormat: на X4 Pro EPUB 2 и EPUB 3 не отличаются, а EPUB 2 читает больше устройств.
const outputFormat = common.OutputFmtEpub2

// Child — тело скрытой подкоманды convert-fb2 <корень> <путь в корне> <формат> <результат>.
// Причина сбоя пишется в stderr и попадает только в лог родителя.
func Child(args []string, stderr io.Writer) int {
	if len(args) != 4 {
		fmt.Fprintln(stderr, "convert-fb2: wrong arguments")
		return exitFailed
	}
	data, code, err := readSource(args[0], args[1], args[2])
	if err == nil {
		code, err = exitFailed, convertBook(data, args[3])
	}
	if err != nil {
		fmt.Fprintln(stderr, "convert-fb2:", err)
		return code
	}
	return 0
}

// readSource читает исходник так же, как скачивание: через os.OpenInRoot, чтобы
// симлинк в корне не вывел за его пределы.
func readSource(root, rel, format string) ([]byte, int, error) {
	f, err := os.OpenInRoot(root, filepath.FromSlash(rel))
	if err != nil {
		return nil, exitSourceOpen, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, exitSourceOpen, err
	}
	data, err := meta.ReadFB2(f, fi.Size(), format)
	if errors.Is(err, meta.ErrTooLarge) {
		return nil, exitTooLarge, err
	}
	return data, exitFailed, err
}

func convertBook(data []byte, out string) (rerr error) {
	defer func() {
		if r := recover(); r != nil {
			rerr = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	ctx := state.ContextWithEnv(context.Background())
	env := state.EnvFromContext(ctx)
	cfg, err := config.LoadConfiguration("")
	if err != nil {
		return err
	}
	// Значение по умолчанию растягивает обложку до 1264×1680 без сохранения пропорций.
	cfg.Document.Images.Cover.Resize = common.ImageResizeModeNone
	log := zap.NewNop()
	env.Cfg, env.Log, env.DefaultStyle = cfg, log, defaultCSS

	// fb2cng разрешает url(...) из стилей книги относительно каталога srcName и кладёт
	// найденный файл в EPUB. Каталог пуст, поэтому ни один файл сервера туда не попадёт.
	empty, err := os.MkdirTemp("", "src-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(empty)

	// BOM снимается, остальное проходит как есть: кодировку читает разбор XML.
	r := transform.NewReader(bytes.NewReader(data), unicode.BOMOverride(encoding.Nop.NewDecoder()))
	c, err := content.Prepare(ctx, r, filepath.Join(empty, "book.fb2"), outputFormat, log)
	if err != nil {
		return err
	}
	defer os.RemoveAll(c.WorkDir)
	return epub.Generate(ctx, c, out, out, &cfg.Document, log)
}
