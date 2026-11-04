// Команда ksb — сервер koreader-sync-board и CLI управления им.
//
//	ksb serve                        запустить сервер
//	ksb user add|list|disable|delete управление пользователями
//	ksb device add|list|revoke       управление устройствами
//	ksb healthcheck                  проверка работоспособности (для Docker HEALTHCHECK)
//	ksb version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/varrcan/koreader-sync-board/server/internal/buildinfo"
	"github.com/varrcan/koreader-sync-board/server/internal/config"
)

const usage = `usage: ksb <command> [arguments]

commands:
  serve                          run the server
  user add <name> [-role admin|user]
  user list
  user disable <name>
  user delete <name>
  device add <user> <device>     prints the device password once
  device list <user>
  device revoke <user> <device>
  healthcheck [-url URL]         exit 0 if the server is healthy
  version

configuration (environment):
  KSB_LISTEN (:8080), KSB_DATA_DIR (/data), KSB_KOSYNC_REGISTRATION (false),
  KSB_KOSYNC_SELF_DELETE (false), KSB_TRUSTED_PROXIES (CIDRs or addresses
  of reverse proxies whose X-Forwarded-For is trusted, comma-separated),
  ADMIN_USER, ADMIN_PASSWORD
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "ksb:", err)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		err = serve(ctx, cfg, rest, logger)
	case "user":
		err = userCmd(ctx, cfg, rest, stdout)
	case "device":
		err = deviceCmd(ctx, cfg, rest, stdout)
	case "healthcheck":
		err = healthcheck(ctx, cfg, rest)
	case "version":
		fmt.Fprintf(stdout, "ksb %s (%s)\n", buildinfo.Version, buildinfo.CommitOrVCS())
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "ksb: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue), errors.Is(err, flag.ErrHelp):
		fmt.Fprintf(stderr, "ksb: %v\n\n%s", err, usage)
		return 2
	default:
		fmt.Fprintln(stderr, "ksb:", err)
		return 1
	}
}

type usageError string

func (e usageError) Error() string { return string(e) }
