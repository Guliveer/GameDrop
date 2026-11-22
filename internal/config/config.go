// Package config loads and validates GameDrop configuration from environment
// variables. It reads env directly (no dotenv library): Fly Machines and local
// shells both provide env, and secrets never belong in the repository.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Guliveer/GameDrop/internal/model"
)

// DefaultUserAgent identifies GameDrop to storefronts and Telegram.
const DefaultUserAgent = "GameDrop/1.0 (+https://github.com/Guliveer/GameDrop)"

// Mode values for APP_MODE.
const (
	ModeDevelopment = "development"
	ModeProduction  = "production"
)

// Config is the fully resolved application configuration.
type Config struct {
	Mode         string
	LogLevel     string
	DatabasePath string

	HTTPTimeout    time.Duration
	HTTPMaxRetries int
	UserAgent      string

	// Countries are the storefront countries to query, uppercased ISO codes.
	// They are independent from the Fly.io region.
	Countries []string
	// Locale is the store locale passed to stores that accept one.
	Locale string

	Steam    SteamConfig
	Epic     EpicConfig
	GOG      GOGConfig
	Prime    PrimeConfig
	Telegram TelegramConfig

	// SourceTimeout bounds a single store/country fetch.
	SourceTimeout time.Duration
	// MaxConcurrency bounds how many store/country fetches run at once.
	MaxConcurrency int
	// NotifyMaxAttempts bounds delivery attempts per notification.
	NotifyMaxAttempts int
}

// SteamConfig configures the Steam adapter.
type SteamConfig struct {
	Enabled bool
}

// EpicConfig configures the Epic Games Store adapter.
type EpicConfig struct {
	Enabled bool
}

// GOGConfig configures the GOG adapter.
type GOGConfig struct {
	Enabled bool
}

// PrimeConfig configures the optional Prime Gaming adapter.
type PrimeConfig struct {
	Enabled bool
}

// TelegramConfig configures Telegram notifications.
type TelegramConfig struct {
	Enabled  bool
	BotToken string
	ChatID   string
}

// Source describes one enabled store adapter.
type Source struct {
	Store   model.Store
	Enabled bool
}

// Sources returns the configured store adapters in a stable order.
func (c Config) Sources() []Source {
	return []Source{
		{Store: model.StoreSteam, Enabled: c.Steam.Enabled},
		{Store: model.StoreEpic, Enabled: c.Epic.Enabled},
		{Store: model.StoreGOG, Enabled: c.GOG.Enabled},
		{Store: model.StorePrime, Enabled: c.Prime.Enabled},
	}
}

// EnabledSources returns only the store adapters that should run.
func (c Config) EnabledSources() []model.Store {
	var out []model.Store
	for _, s := range c.Sources() {
		if s.Enabled {
			out = append(out, s.Store)
		}
	}
	return out
}

// Load reads the environment and applies defaults.
func Load() Config {
	mode := envOr("APP_MODE", ModeDevelopment)
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	chatID := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))

	c := Config{
		Mode:              mode,
		LogLevel:          strings.ToLower(envOr("LOG_LEVEL", "info")),
		DatabasePath:      envOr("DATABASE_PATH", defaultDatabasePath(mode)),
		HTTPTimeout:       durationOr("HTTP_TIMEOUT_SEC", 30*time.Second),
		HTTPMaxRetries:    intOr("HTTP_MAX_RETRIES", 2),
		UserAgent:         envOr("HTTP_USER_AGENT", DefaultUserAgent),
		Countries:         parseCountries(envOr("STORE_COUNTRIES", "PL")),
		Locale:            envOr("STORE_LOCALE", "pl-PL"),
		Steam:             SteamConfig{Enabled: boolOr("ENABLE_STEAM", true)},
		Epic:              EpicConfig{Enabled: boolOr("ENABLE_EPIC", true)},
		GOG:               GOGConfig{Enabled: boolOr("ENABLE_GOG", true)},
		Prime:             PrimeConfig{Enabled: boolOr("ENABLE_PRIME", false)},
		SourceTimeout:     durationOr("SOURCE_TIMEOUT_SEC", 45*time.Second),
		MaxConcurrency:    intOr("MAX_CONCURRENCY", 4),
		NotifyMaxAttempts: intOr("NOTIFY_MAX_ATTEMPTS", 3),
		Telegram: TelegramConfig{
			BotToken: token,
			ChatID:   chatID,
		},
	}

	// Notifications are enabled automatically when both credentials are
	// present. ENABLE_TELEGRAM=false forces them off, and an explicit
	// ENABLE_TELEGRAM=true without credentials is a configuration error that
	// Validate reports precisely.
	c.Telegram.Enabled = token != "" && chatID != ""
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ENABLE_TELEGRAM"))) {
	case "1", "true", "yes", "on":
		c.Telegram.Enabled = true
	case "0", "false", "no", "off":
		c.Telegram.Enabled = false
	}

	return c
}

// Validate checks the configuration before a collection cycle starts. It
// returns an error describing every problem found, so operators can fix the
// environment in one pass.
func (c Config) Validate() error {
	var problems []string

	if c.Mode != ModeDevelopment && c.Mode != ModeProduction {
		problems = append(problems, fmt.Sprintf("APP_MODE must be %q or %q, got %q", ModeDevelopment, ModeProduction, c.Mode))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("LOG_LEVEL must be one of debug, info, warn, error; got %q", c.LogLevel))
	}
	if strings.TrimSpace(c.DatabasePath) == "" {
		problems = append(problems, "DATABASE_PATH must not be empty")
	}
	if c.HTTPTimeout <= 0 {
		problems = append(problems, "HTTP_TIMEOUT_SEC must be greater than zero")
	}
	if c.HTTPMaxRetries < 0 {
		problems = append(problems, "HTTP_MAX_RETRIES must be zero or greater")
	}
	if c.SourceTimeout <= 0 {
		problems = append(problems, "SOURCE_TIMEOUT_SEC must be greater than zero")
	}
	if c.MaxConcurrency < 1 {
		problems = append(problems, "MAX_CONCURRENCY must be at least 1")
	}
	if c.NotifyMaxAttempts < 1 {
		problems = append(problems, "NOTIFY_MAX_ATTEMPTS must be at least 1")
	}
	if len(c.Countries) == 0 {
		problems = append(problems, "STORE_COUNTRIES must contain at least one ISO 3166-1 alpha-2 country code")
	}
	for _, country := range c.Countries {
		if len(country) != 2 {
			problems = append(problems, fmt.Sprintf("STORE_COUNTRIES entry %q is not a two-letter country code", country))
		}
	}
	if strings.TrimSpace(c.Locale) == "" {
		problems = append(problems, "STORE_LOCALE must not be empty")
	}
	if len(c.EnabledSources()) == 0 {
		problems = append(problems, "at least one store must be enabled (ENABLE_STEAM, ENABLE_EPIC, ENABLE_GOG, ENABLE_PRIME)")
	}
	if c.Telegram.Enabled {
		if c.Telegram.BotToken == "" {
			problems = append(problems, "TELEGRAM_BOT_TOKEN is required when Telegram notifications are enabled")
		}
		if c.Telegram.ChatID == "" {
			problems = append(problems, "TELEGRAM_CHAT_ID is required when Telegram notifications are enabled")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// defaultDatabasePath keeps the production default on the Fly Volume while
// local development gets a file in the working directory.
func defaultDatabasePath(mode string) string {
	if mode == ModeProduction {
		return "/data/offers.db"
	}
	return "gamedrop.db"
}

// parseCountries splits, uppercases, and de-duplicates a comma separated list.
func parseCountries(raw string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, part := range strings.Split(raw, ",") {
		code := strings.ToUpper(strings.TrimSpace(part))
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func durationOr(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	// Accept a bare number of seconds, the original HTTP_TIMEOUT_SEC format.
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return def
}

func intOr(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func boolOr(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	return parseBool(raw, def)
}

func parseBool(raw string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
