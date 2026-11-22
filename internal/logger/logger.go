// Package logger builds the application's structured logger on top of
// log/slog. It is the single place that decides output format and destination.
package logger

import (
	"log/slog"
	"os"
	"strings"

	"github.com/Guliveer/GameDrop/internal/config"
)

// Init returns a *slog.Logger writing to stdout. Production uses JSON so Fly
// logs are easy to ingest; development uses human readable text.
func Init(mode, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: ParseLevel(level)}
	if mode == config.ModeProduction {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// ParseLevel maps a level name to a slog.Level, defaulting to info.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
