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

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Error("db open", "err", err)
		os.Exit(1)
	}
	defer database.Close()
	log.Info("database opened", "path", cfg.DBPath)

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
	handler := api.NewHandler(cfg, database, sabClient, webFS)
	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      handler.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Background sync engine
	syncEngine := syncer.New(cfg, database, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go syncEngine.Start(ctx)

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
	cancel() // stop sync engine

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("server shutdown", "err", err)
	}
	log.Info("bye")
}
