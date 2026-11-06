package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/apiv1"
	"github.com/varrcan/koreader-sync-board/server/internal/buildinfo"
	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
	"github.com/varrcan/koreader-sync-board/server/internal/config"
	"github.com/varrcan/koreader-sync-board/server/internal/kosync"
	"github.com/varrcan/koreader-sync-board/server/internal/panelauth"
	"github.com/varrcan/koreader-sync-board/server/internal/progress"
	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
	"github.com/varrcan/koreader-sync-board/server/internal/server"
	"github.com/varrcan/koreader-sync-board/server/internal/statistics"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
	"github.com/varrcan/koreader-sync-board/server/internal/web"
	"github.com/varrcan/koreader-sync-board/server/internal/webdav"
)

func serve(ctx context.Context, cfg config.Config, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "listen address (KSB_LISTEN)")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "data directory (KSB_DATA_DIR)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	db, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	// Один ограничитель на kosync, WebDAV и панель: подбор пароля панели и ключа
	// устройства с одного адреса расходует общий бюджет.
	limiter := ratelimit.New(log)
	acc := accounts.New(db, accounts.WithLimiter(limiter))
	created, err := acc.Bootstrap(ctx, cfg.AdminUser, cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if created {
		log.Info("created admin user from ADMIN_USER", "user", cfg.AdminUser)
	}
	setup := panelauth.NewSetup(acc, limiter)
	users, err := acc.CountUsers(ctx)
	if err != nil {
		return err
	}
	if users == 0 {
		log.Warn("setup required", "setup_code", setup.Generate(), "url", "/setup")
	}

	ip := clientip.New(cfg.TrustedProxies)
	prog := progress.New(db)
	stats := statistics.New(db, cfg.DataDir, log)
	handler := server.New(server.Handlers{
		Kosync: kosync.New(acc, prog, kosync.Options{
			Registration: cfg.KosyncRegistration,
			SelfDelete:   cfg.KosyncSelfDelete,
			ClientIP:     ip,
		}, log),
		API: apiv1.NewHandler(&apiv1.Server{
			Accounts:   acc,
			Auth:       panelauth.New(acc, ip, log),
			Setup:      setup,
			Progress:   prog,
			Statistics: stats,
			Log:        log,
		}),
		WebDAV: webdav.New(acc, stats, ip, log),
		Web:    web.Handler(),
	})
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("server started", "listen", cfg.Listen, "data_dir", cfg.DataDir,
		"version", buildinfo.Version, "kosync_registration", cfg.KosyncRegistration,
		"kosync_self_delete", cfg.KosyncSelfDelete, "trusted_proxies", fmt.Sprint(cfg.TrustedProxies))

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openDB открывает базу в каталоге данных, создавая каталог при необходимости.
func openDB(ctx context.Context, cfg config.Config) (*store.DB, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	return store.Open(ctx, filepath.Join(cfg.DataDir, store.FileName))
}
