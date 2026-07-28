package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spottr/spottr/internal/api"
	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/db"
	"github.com/spottr/spottr/internal/nntp"
	"github.com/spottr/spottr/internal/sabnzbd"
	syncer "github.com/spottr/spottr/internal/sync"
	"github.com/spottr/spottr/internal/webstatic"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	// Size the SQLite pool alongside the NNTP pool: a request blocked on an
	// upstream article still holds nothing from the database, but concurrent
	// browse traffic needs more than the single connection this used to have.
	dbConns := cfg.NNTPMaxConns + 4
	database, err := db.Open(cfg.DBPath, dbConns)
	if err != nil {
		log.Error("db open", "err", err)
		os.Exit(1)
	}
	defer database.Close()
	log.Info("database opened", "path", cfg.DBPath, "max_conns", dbConns)

	// Newznab API key: persisted, so it is stable across restarts and
	// container recreation.
	apiKey, generated, err := api.ResolveAPIKey(cfg, database)
	if err != nil {
		log.Error("api key", "err", err)
		os.Exit(1)
	}
	switch {
	case cfg.APIKey != "":
		log.Info("newznab API key taken from API_KEY environment variable")
	case generated:
		// Logged exactly once, on the run that created it, so the operator can
		// configure their indexer. Never logged again.
		log.Info("generated newznab API key (store this; it will not be shown again)",
			"api_key", apiKey)
	default:
		log.Info("newznab API key loaded from database")
	}

	if cfg.AuthMode != config.AuthModePassword {
		log.Warn("in-app authentication is disabled",
			"auth_mode", cfg.AuthMode,
			"detail", "privileged endpoints are reachable by anything that can connect; ensure an authenticating proxy is the only route in")
	}

	// Shared, bounded NNTP connection pool. Every article fetch used to dial
	// and authenticate a fresh connection, so request volume translated
	// directly into upstream connection count.
	pool := nntp.NewPool(cfg.NNTPHost, cfg.NNTPPort, cfg.NNTPTLS, cfg.NNTPUser, cfg.NNTPPass, cfg.NNTPMaxConns)
	log.Info("nntp pool ready", "host", cfg.NNTPHost, "max_conns", cfg.NNTPMaxConns)

	// SABnzbd client (optional — only if host is configured)
	var sabClient *sabnzbd.Client
	if cfg.SABHost != "" {
		sabClient = sabnzbd.New(cfg)
		if err := sabClient.Ping(); err != nil {
			log.Warn("SABnzbd unreachable at startup", "err", err)
		} else {
			log.Info("SABnzbd connected", "host", cfg.SABHost)
		}
	}

	// HTTP server
	webFS, err := fs.Sub(webstatic.FS, "dist")
	if err != nil {
		log.Error("web embed error", "err", err)
		os.Exit(1)
	}
	handler := api.NewHandler(cfg, database, sabClient, webFS, pool, apiKey, log)
	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler.Router(),
		// ReadHeaderTimeout guards against slow-header attacks; the body
		// allowance is separate so large POSTs are not penalised.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// A cold NZB costs an NNTP dial, auth, a multi-megabyte article read
		// and an inflate. The old 30s ceiling could truncate that mid-response.
		WriteTimeout:   120 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Background workers
	syncEngine := syncer.New(cfg, database, log, pool)
	go syncEngine.Start(ctx)
	if cfg.BackfillEnabled {
		go (&syncer.Backfill{Engine: syncEngine}).Start(ctx)
	}
	go syncer.NewMaintainer(cfg, database, log).Start(ctx)
	go pool.RunReaper(ctx)

	// Start HTTP server
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutting down...")
	cancel() // stop background workers

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("server shutdown", "err", err)
	}
	pool.Close()
	log.Info("bye")
}
