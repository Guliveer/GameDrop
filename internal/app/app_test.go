package app

import (
	"context"
	"testing"
)

// clearEnv neutralizes the variables config.Load reads.
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

// TestRunInvalidConfiguration verifies the documented exit code 2. It never
// reaches the network or touches a database.
func TestRunInvalidConfiguration(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_MODE", "staging")

	if code := Run(context.Background()); code != 2 {
		t.Fatalf("exit code = %d, want 2 for invalid configuration", code)
	}
}

// TestRunStorageFailure verifies that an unusable database path produces exit
// code 1 before any store is contacted.
func TestRunStorageFailure(t *testing.T) {
	clearEnv(t)
	// A directory is not a valid SQLite file, so opening it fails.
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_PATH", t.TempDir())
	t.Setenv("STORE_COUNTRIES", "PL")
	t.Setenv("ENABLE_STEAM", "true")

	if code := Run(context.Background()); code != 1 {
		t.Fatalf("exit code = %d, want 1 for a storage failure", code)
	}
}
