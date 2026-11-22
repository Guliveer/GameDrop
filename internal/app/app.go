// Package app holds the collection cycle that the gamedrop binary runs. It is a
// separate package so that the setup wizard can run the very same cycle in
// process instead of spawning the binary again.
package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Guliveer/GameDrop/internal/collector"
	"github.com/Guliveer/GameDrop/internal/config"
	"github.com/Guliveer/GameDrop/internal/envfile"
	"github.com/Guliveer/GameDrop/internal/httpclient"
	"github.com/Guliveer/GameDrop/internal/logger"
	"github.com/Guliveer/GameDrop/internal/notify"
	"github.com/Guliveer/GameDrop/internal/storage"
	"github.com/Guliveer/GameDrop/internal/stores"
	"github.com/Guliveer/GameDrop/internal/stores/epic"
	"github.com/Guliveer/GameDrop/internal/stores/gog"
	"github.com/Guliveer/GameDrop/internal/stores/prime"
	"github.com/Guliveer/GameDrop/internal/stores/steam"
)

// Run executes one collection cycle and returns the process exit code:
//
//	0 - the cycle completed; every configured source succeeded, or at least one
//	    did while others failed (a partial run is not a failure)
//	1 - the cycle could not complete (storage error, or every source failed)
//	2 - configuration is invalid
func Run(ctx context.Context) int {
	// Load the optional .env file before configuration is read. Variables that
	// already exist in the environment always win, and a missing default file
	// is not an error.
	envFile, err := envfile.LoadDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gamedrop: configuration error:\n%v\n", err)
		return 2
	}

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "gamedrop: configuration error:\n%v\n", err)
		return 2
	}

	log := logger.Init(cfg.Mode, cfg.LogLevel).With("app", "gamedrop", "mode", cfg.Mode)
	if envFile.Found {
		log.Info("environment file loaded",
			"path", envFile.Path,
			"variables_loaded", envFile.Loaded,
			"variables_already_set", envFile.Skipped,
		)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := httpclient.New(httpclient.Options{
		Timeout:    cfg.HTTPTimeout,
		MaxRetries: cfg.HTTPMaxRetries,
		UserAgent:  cfg.UserAgent,
	})

	db, err := storage.Open(ctx, storage.Options{Path: cfg.DatabasePath})
	if err != nil {
		log.ErrorContext(ctx, "cannot open database", "path", cfg.DatabasePath, "error", err)
		return 1
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.WarnContext(ctx, "closing database", "error", err)
		}
	}()

	var notifier notify.Sender
	if cfg.Telegram.Enabled {
		notifier = notify.NewTelegram(client, cfg.Telegram.BotToken, cfg.Telegram.ChatID)
	}

	c := collector.New(collector.Options{
		DB:                db,
		Registry:          buildRegistry(cfg, client),
		Countries:         cfg.Countries,
		Locale:            cfg.Locale,
		SourceTimeout:     cfg.SourceTimeout,
		MaxConcurrency:    cfg.MaxConcurrency,
		Notifier:          notifier,
		MaxNotifyAttempts: cfg.NotifyMaxAttempts,
		Logger:            log,
		Now:               time.Now,
	})

	summary, err := c.Run(ctx)
	if err != nil {
		log.ErrorContext(ctx, "collection cycle failed",
			"run_id", summary.RunID,
			"status", summary.Status,
			"duration_ms", summary.Duration().Milliseconds(),
			"sources_ok", summary.SourcesOK,
			"sources_failed", summary.SourcesFailed,
			"failures", summary.Failures,
			"error", err,
		)
		return 1
	}

	if summary.Status == storage.RunStatusPartial {
		log.WarnContext(ctx, "collection cycle partially succeeded; some sources failed",
			"run_id", summary.RunID, "failures", summary.Failures)
	}
	return 0
}

// buildRegistry constructs the adapters enabled by configuration.
func buildRegistry(cfg config.Config, client *httpclient.Client) *stores.Registry {
	var adapters []stores.Adapter
	if cfg.Steam.Enabled {
		adapters = append(adapters, steam.New(client))
	}
	if cfg.Epic.Enabled {
		adapters = append(adapters, epic.New(client))
	}
	if cfg.GOG.Enabled {
		adapters = append(adapters, gog.New(client))
	}
	if cfg.Prime.Enabled {
		adapters = append(adapters, prime.New())
	}
	return stores.NewRegistry(adapters...)
}
