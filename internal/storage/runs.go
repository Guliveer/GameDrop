package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Guliveer/GameDrop/internal/model"
)

// Run statuses persisted in the runs table.
const (
	RunStatusSucceeded = "succeeded"
	RunStatusPartial   = "partial"
	RunStatusFailed    = "failed"
)

// StartRun records the beginning of a collection cycle.
func (d *DB) StartRun(ctx context.Context, id string, at time.Time) error {
	if id == "" {
		return errors.New("storage: run id is empty")
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO runs (id, started_at) VALUES (?, ?)`, id, at.UTC().Unix())
	if err != nil {
		return fmt.Errorf("storage: start run %s: %w", id, err)
	}
	return nil
}

// RunSummary is the persisted outcome of a collection cycle.
type RunSummary struct {
	ID                  string
	Status              string
	SourcesOK           int
	SourcesFailed       int
	OffersSeen          int
	OffersNew           int
	OffersChanged       int
	OffersExpired       int
	NotificationsSent   int
	NotificationsFailed int
	Error               string
}

// FinishRun stores the final outcome of a run.
func (d *DB) FinishRun(ctx context.Context, s RunSummary, at time.Time) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE runs SET
			finished_at = ?, status = ?, sources_ok = ?, sources_failed = ?, offers_seen = ?,
			offers_new = ?, offers_changed = ?, offers_expired = ?, notifications_sent = ?,
			notifications_failed = ?, error = ?
		WHERE id = ?`,
		at.UTC().Unix(), s.Status, s.SourcesOK, s.SourcesFailed, s.OffersSeen, s.OffersNew,
		s.OffersChanged, s.OffersExpired, s.NotificationsSent, s.NotificationsFailed,
		nullableString(s.Error), s.ID)
	if err != nil {
		return fmt.Errorf("storage: finish run %s: %w", s.ID, err)
	}
	return nil
}

// SourceRun is the recorded outcome of one store/country fetch.
type SourceRun struct {
	RunID       string
	Source      model.Store
	Country     string
	Status      model.SourceStatus
	OffersFound int
	Duration    time.Duration
	Error       string
	StartedAt   time.Time
	FinishedAt  time.Time
}

// RecordSourceRun stores the result of one adapter fetch and updates the
// per-source state (last success, last error).
func (d *DB) RecordSourceRun(ctx context.Context, r SourceRun) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin source run: %w", err)
	}
	defer tx.Rollback()

	country := normalizeCountry(r.Country)
	_, err = tx.ExecContext(ctx, `INSERT INTO source_runs
		(run_id, source, country, status, offers_found, duration_ms, error, started_at, finished_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		r.RunID, string(r.Source), country, string(r.Status), r.OffersFound,
		r.Duration.Milliseconds(), nullableString(r.Error),
		r.StartedAt.UTC().Unix(), r.FinishedAt.UTC().Unix())
	if err != nil {
		return fmt.Errorf("storage: insert source run: %w", err)
	}

	lastSuccess := sql.NullInt64{}
	if r.Status == model.SourceSuccess || r.Status == model.SourcePartial {
		lastSuccess = sql.NullInt64{Int64: r.FinishedAt.UTC().Unix(), Valid: true}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_state
		(source, country, status, last_success_at, last_attempt_at, last_error)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(source, country) DO UPDATE SET
			status = excluded.status,
			last_success_at = COALESCE(excluded.last_success_at, source_state.last_success_at),
			last_attempt_at = excluded.last_attempt_at,
			last_error = excluded.last_error`,
		string(r.Source), country, string(r.Status), lastSuccess,
		r.FinishedAt.UTC().Unix(), nullableString(r.Error))
	if err != nil {
		return fmt.Errorf("storage: upsert source state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit source run: %w", err)
	}
	return nil
}

// SourceState is the last known status of a store/country adapter.
type SourceState struct {
	Source        model.Store
	Country       string
	Status        model.SourceStatus
	LastSuccessAt *time.Time
	LastAttemptAt time.Time
	LastError     string
}

// GetSourceState reads the persisted state for one store/country pair.
func (d *DB) GetSourceState(ctx context.Context, store model.Store, country string) (SourceState, bool, error) {
	var (
		s           SourceState
		status      string
		lastAttempt int64
		lastSuccess sql.NullInt64
		lastErr     sql.NullString
	)
	err := d.sql.QueryRowContext(ctx, `SELECT status, last_success_at, last_attempt_at, last_error
		FROM source_state WHERE source = ? AND country = ?`,
		string(store), normalizeCountry(country)).
		Scan(&status, &lastSuccess, &lastAttempt, &lastErr)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceState{}, false, nil
	}
	if err != nil {
		return SourceState{}, false, fmt.Errorf("storage: read source state: %w", err)
	}
	s.Source = store
	s.Country = normalizeCountry(country)
	s.Status = model.ParseSourceStatus(status)
	s.LastSuccessAt = timePtr(lastSuccess)
	s.LastAttemptAt = time.Unix(lastAttempt, 0).UTC()
	if lastErr.Valid {
		s.LastError = lastErr.String
	}
	return s, true, nil
}

// RunRecord is a stored collection run, used in tests and diagnostics.
type RunRecord struct {
	ID                  string
	StartedAt           time.Time
	FinishedAt          *time.Time
	Status              string
	SourcesOK           int
	SourcesFailed       int
	OffersSeen          int
	OffersNew           int
	OffersChanged       int
	OffersExpired       int
	NotificationsSent   int
	NotificationsFailed int
	Error               string
}

// GetRun loads a stored run by ID.
func (d *DB) GetRun(ctx context.Context, id string) (RunRecord, bool, error) {
	var (
		r          RunRecord
		startedAt  int64
		finishedAt sql.NullInt64
		status     sql.NullString
		errText    sql.NullString
	)
	err := d.sql.QueryRowContext(ctx, `SELECT id, started_at, finished_at, status, sources_ok, sources_failed,
			offers_seen, offers_new, offers_changed, offers_expired, notifications_sent,
			notifications_failed, error
		FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &startedAt, &finishedAt, &status, &r.SourcesOK, &r.SourcesFailed,
			&r.OffersSeen, &r.OffersNew, &r.OffersChanged, &r.OffersExpired,
			&r.NotificationsSent, &r.NotificationsFailed, &errText)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, false, nil
	}
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("storage: read run: %w", err)
	}
	r.StartedAt = time.Unix(startedAt, 0).UTC()
	r.FinishedAt = timePtr(finishedAt)
	if status.Valid {
		r.Status = status.String
	}
	if errText.Valid {
		r.Error = errText.String
	}
	return r, true, nil
}

// SourceRunRecord is one stored adapter result.
type SourceRunRecord struct {
	RunID       string
	Source      string
	Country     string
	Status      string
	OffersFound int
	DurationMS  int64
	Error       string
}

// SourceRuns returns the adapter results recorded for a run.
func (d *DB) SourceRuns(ctx context.Context, runID string) ([]SourceRunRecord, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT run_id, source, country, status, offers_found, duration_ms, error
		FROM source_runs WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("storage: select source runs: %w", err)
	}
	defer rows.Close()

	var out []SourceRunRecord
	for rows.Next() {
		var (
			rec    SourceRunRecord
			errTxt sql.NullString
		)
		if err := rows.Scan(&rec.RunID, &rec.Source, &rec.Country, &rec.Status, &rec.OffersFound, &rec.DurationMS, &errTxt); err != nil {
			return nil, fmt.Errorf("storage: scan source run: %w", err)
		}
		if errTxt.Valid {
			rec.Error = errTxt.String
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate source runs: %w", err)
	}
	return out, nil
}
