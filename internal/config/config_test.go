package config

import (
	"strings"
	"testing"
	"time"
)

// clearEnv neutralizes every variable Load reads so tests see documented
// defaults rather than the developer's shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"APP_MODE", "LOG_LEVEL", "DATABASE_PATH", "HTTP_TIMEOUT_SEC", "HTTP_MAX_RETRIES",
		"HTTP_USER_AGENT", "STORE_COUNTRIES", "STORE_LOCALE", "ENABLE_STEAM", "ENABLE_EPIC",
		"ENABLE_GOG", "ENABLE_PRIME", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID",
		"ENABLE_TELEGRAM", "SOURCE_TIMEOUT_SEC", "MAX_CONCURRENCY", "NOTIFY_MAX_ATTEMPTS",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	cfg := Load()

	if cfg.Mode != ModeDevelopment {
		t.Errorf("Mode = %q", cfg.Mode)
	}
	if cfg.DatabasePath != "gamedrop.db" {
		t.Errorf("DatabasePath = %q, want the development default", cfg.DatabasePath)
	}
	if len(cfg.Countries) != 1 || cfg.Countries[0] != "PL" {
		t.Errorf("Countries = %v, want [PL]", cfg.Countries)
	}
	if cfg.Locale != "pl-PL" {
		t.Errorf("Locale = %q", cfg.Locale)
	}
	if !cfg.Steam.Enabled || !cfg.Epic.Enabled || !cfg.GOG.Enabled {
		t.Errorf("Steam/Epic/GOG should be enabled by default: %+v", cfg)
	}
	if cfg.Prime.Enabled {
		t.Error("Prime Gaming must be disabled by default")
	}
	if cfg.Telegram.Enabled {
		t.Error("Telegram must be disabled when credentials are absent")
	}
	if cfg.HTTPTimeout != 30*time.Second {
		t.Errorf("HTTPTimeout = %s", cfg.HTTPTimeout)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default configuration must validate: %v", err)
	}
}

func TestLoadProductionDatabaseDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_MODE", "production")
	cfg := Load()
	if cfg.DatabasePath != "/data/offers.db" {
		t.Errorf("DatabasePath = %q, want /data/offers.db", cfg.DatabasePath)
	}
}

func TestCountriesParsing(t *testing.T) {
	clearEnv(t)
	t.Setenv("STORE_COUNTRIES", "pl, US ,pl,de")
	cfg := Load()
	if got := strings.Join(cfg.Countries, ","); got != "PL,US,DE" {
		t.Errorf("Countries = %q, want PL,US,DE", got)
	}
}

func TestTelegramAutoEnable(t *testing.T) {
	clearEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_CHAT_ID", "123")
	cfg := Load()
	if !cfg.Telegram.Enabled {
		t.Fatal("Telegram should auto-enable when both credentials are set")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestTelegramExplicitlyEnabledWithoutCredentials(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENABLE_TELEGRAM", "true")
	cfg := Load()
	if !cfg.Telegram.Enabled {
		t.Fatal("ENABLE_TELEGRAM=true should keep the channel enabled so validation can report the problem")
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected a validation error for missing Telegram credentials")
	}
	if !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("error = %v", err)
	}
}

func TestTelegramDisabledWithCredentials(t *testing.T) {
	clearEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_CHAT_ID", "123")
	t.Setenv("ENABLE_TELEGRAM", "false")
	cfg := Load()
	if cfg.Telegram.Enabled {
		t.Fatal("ENABLE_TELEGRAM=false must disable notifications")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidationCatchesBadValues(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantSub string
	}{
		{"bad mode", map[string]string{"APP_MODE": "staging"}, "APP_MODE"},
		{"bad log level", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL"},
		{"no countries", map[string]string{"STORE_COUNTRIES": ","}, "STORE_COUNTRIES"},
		{"bad country code", map[string]string{"STORE_COUNTRIES": "POL"}, "two-letter"},
		{"no sources", map[string]string{
			"ENABLE_STEAM": "false", "ENABLE_EPIC": "false", "ENABLE_GOG": "false", "ENABLE_PRIME": "false",
		}, "at least one store"},
		{"zero concurrency", map[string]string{"MAX_CONCURRENCY": "0"}, "MAX_CONCURRENCY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			err := Load().Validate()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestDurationAcceptsGoSyntaxAndSeconds(t *testing.T) {
	clearEnv(t)
	t.Setenv("HTTP_TIMEOUT_SEC", "1500ms")
	if got := Load().HTTPTimeout; got != 1500*time.Millisecond {
		t.Errorf("HTTPTimeout = %s, want 1.5s", got)
	}

	clearEnv(t)
	t.Setenv("HTTP_TIMEOUT_SEC", "12")
	if got := Load().HTTPTimeout; got != 12*time.Second {
		t.Errorf("HTTPTimeout = %s, want 12s", got)
	}
}

func TestEnabledSourcesOrder(t *testing.T) {
	clearEnv(t)
	cfg := Load()
	sources := cfg.EnabledSources()
	want := []string{"steam", "epic", "gog"}
	if len(sources) != len(want) {
		t.Fatalf("EnabledSources = %v", sources)
	}
	for i, store := range sources {
		if string(store) != want[i] {
			t.Errorf("source %d = %q, want %q", i, store, want[i])
		}
	}
}
