// Package storage implements GameDrop's SQLite persistence: offers, offer
// history, per-source run results, collection runs, and the notification
// outbox. All writes happen in transactions so a discovered notification
// intent is atomic with the offer change that produced it.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, works with CGO_ENABLED=0
)

// DB wraps the SQLite connection pool.
//
// The pool is deliberately limited to a single connection: GameDrop is a
// single-process, single-cycle worker, and one writer removes SQLITE_BUSY
// contention entirely.
type DB struct {
	sql *sql.DB
	now func() time.Time
}

// Options configures Open.
type Options struct {
	// Path is the SQLite database file. Parent directories are created.
	Path string
	// Now overrides the clock (tests only).
	Now func() time.Time
	// BusyTimeout is how long SQLite waits for a lock. Defaults to 5s.
	BusyTimeout time.Duration
}

// Open creates the database file if needed, applies pragmas, and runs
// migrations. It is safe to call on an existing database.
func Open(ctx context.Context, opts Options) (*DB, error) {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		return nil, errors.New("storage: database path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("storage: create database directory %q: %w", dir, err)
		}
	}

	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage: open %q: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	sqlDB.SetConnMaxIdleTime(0)

	busy := opts.BusyTimeout
	if busy <= 0 {
		busy = 5 * time.Second
	}
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		fmt.Sprintf("PRAGMA busy_timeout=%d", busy.Milliseconds()),
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	}
	for _, pragma := range pragmas {
		if _, err := sqlDB.ExecContext(ctx, pragma); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("storage: apply %q: %w", pragma, err)
		}
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	db := &DB{sql: sqlDB, now: now}
	if err := db.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Close releases the database handle.
func (d *DB) Close() error {
	return d.sql.Close()
}

// migration is one versioned, idempotent schema step.
type migration struct {
	version    int
	name       string
	statements []string
}

const offerColumns = `id, store, country, store_offer_id, title, description, url, image_url,
	offer_type, availability, price_minor, price_currency, original_minor, original_currency,
	starts_at, ends_at, source, fingerprint, first_seen_at, last_seen_at, last_verified_at,
	missing_since, missed_scans, expired_at`

var migrations = []migration{
	{
		version: 1,
		name:    "initial schema",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS offers (
				id                INTEGER PRIMARY KEY AUTOINCREMENT,
				store             TEXT    NOT NULL,
				country           TEXT    NOT NULL DEFAULT '',
				store_offer_id    TEXT    NOT NULL,
				title             TEXT    NOT NULL,
				description       TEXT    NOT NULL DEFAULT '',
				url               TEXT    NOT NULL DEFAULT '',
				image_url         TEXT    NOT NULL DEFAULT '',
				offer_type        TEXT    NOT NULL,
				availability      TEXT    NOT NULL,
				price_minor       INTEGER,
				price_currency    TEXT    NOT NULL DEFAULT '',
				original_minor    INTEGER,
				original_currency TEXT    NOT NULL DEFAULT '',
				starts_at         INTEGER,
				ends_at           INTEGER,
				source            TEXT    NOT NULL DEFAULT '',
				fingerprint       TEXT    NOT NULL,
				first_seen_at     INTEGER NOT NULL,
				last_seen_at      INTEGER NOT NULL,
				last_verified_at  INTEGER,
				missing_since     INTEGER,
				missed_scans      INTEGER NOT NULL DEFAULT 0,
				expired_at        INTEGER,
				UNIQUE (store, country, store_offer_id)
			)`,
			`CREATE INDEX IF NOT EXISTS offers_store_idx ON offers (store, country)`,
			`CREATE INDEX IF NOT EXISTS offers_availability_idx ON offers (availability)`,
			`CREATE INDEX IF NOT EXISTS offers_ends_at_idx ON offers (ends_at)`,

			`CREATE TABLE IF NOT EXISTS offer_history (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				offer_id    INTEGER NOT NULL REFERENCES offers(id) ON DELETE CASCADE,
				run_id      TEXT    NOT NULL,
				change      TEXT    NOT NULL,
				snapshot    TEXT    NOT NULL,
				recorded_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS offer_history_offer_idx ON offer_history (offer_id, recorded_at)`,
			`CREATE INDEX IF NOT EXISTS offer_history_run_idx ON offer_history (run_id)`,

			`CREATE TABLE IF NOT EXISTS runs (
				id                   TEXT PRIMARY KEY,
				started_at           INTEGER NOT NULL,
				finished_at          INTEGER,
				status               TEXT,
				sources_ok           INTEGER NOT NULL DEFAULT 0,
				sources_failed       INTEGER NOT NULL DEFAULT 0,
				offers_seen          INTEGER NOT NULL DEFAULT 0,
				offers_new           INTEGER NOT NULL DEFAULT 0,
				offers_changed       INTEGER NOT NULL DEFAULT 0,
				offers_expired       INTEGER NOT NULL DEFAULT 0,
				notifications_sent   INTEGER NOT NULL DEFAULT 0,
				notifications_failed INTEGER NOT NULL DEFAULT 0,
				error                TEXT
			)`,

			`CREATE TABLE IF NOT EXISTS source_runs (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				run_id       TEXT    NOT NULL,
				source       TEXT    NOT NULL,
				country      TEXT    NOT NULL DEFAULT '',
				status       TEXT    NOT NULL,
				offers_found INTEGER NOT NULL DEFAULT 0,
				duration_ms  INTEGER NOT NULL DEFAULT 0,
				error        TEXT,
				started_at   INTEGER NOT NULL,
				finished_at  INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS source_runs_run_idx ON source_runs (run_id)`,

			`CREATE TABLE IF NOT EXISTS source_state (
				source          TEXT    NOT NULL,
				country         TEXT    NOT NULL DEFAULT '',
				status          TEXT    NOT NULL,
				last_success_at INTEGER,
				last_attempt_at INTEGER NOT NULL,
				last_error      TEXT,
				PRIMARY KEY (source, country)
			)`,

			`CREATE TABLE IF NOT EXISTS notifications (
				id              INTEGER PRIMARY KEY AUTOINCREMENT,
				offer_id        INTEGER NOT NULL REFERENCES offers(id) ON DELETE CASCADE,
				run_id          TEXT    NOT NULL,
				kind            TEXT    NOT NULL,
				dedupe_key      TEXT    NOT NULL UNIQUE,
				payload         TEXT    NOT NULL,
				state           TEXT    NOT NULL,
				attempts        INTEGER NOT NULL DEFAULT 0,
				last_error      TEXT,
				next_attempt_at INTEGER,
				created_at      INTEGER NOT NULL,
				sent_at         INTEGER,
				updated_at      INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS notifications_state_idx ON notifications (state, next_attempt_at)`,
			`CREATE INDEX IF NOT EXISTS notifications_offer_idx ON notifications (offer_id)`,
		},
	},
}

// migrate applies pending migrations. Each migration runs in its own
// transaction and is recorded by version, so repeated runs are no-ops.
func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("storage: create schema_migrations: %w", err)
	}

	for _, m := range migrations {
		var applied int
		if err := d.sql.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, m.version).Scan(&applied); err != nil {
			return fmt.Errorf("storage: check migration %d: %w", m.version, err)
		}
		if applied > 0 {
			continue
		}

		tx, err := d.sql.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("storage: begin migration %d: %w", m.version, err)
		}
		for _, stmt := range m.statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("storage: migration %d (%s): %w", m.version, m.name, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, d.now().UTC().Unix()); err != nil {
			tx.Rollback()
			return fmt.Errorf("storage: record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("storage: commit migration %d: %w", m.version, err)
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := d.sql.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("storage: read schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// timeToNull converts a time to a nullable unix timestamp (zero = NULL).
func timeToNull(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().Unix(), Valid: true}
}

// timeFromNull converts a nullable unix timestamp to a time (NULL = zero).
func timeFromNull(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(n.Int64, 0).UTC()
}

// timePtr converts a nullable timestamp to *time.Time for query results.
func timePtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(n.Int64, 0).UTC()
	return &t
}

func nullableString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
