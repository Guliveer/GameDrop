package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Guliveer/GameDrop/internal/model"
)

// Notification is one outbox row.
type Notification struct {
	ID        int64
	OfferID   int64
	RunID     string
	Kind      string
	DedupeKey string
	Payload   string
	State     model.NotificationState
	Attempts  int
	LastError string
	CreatedAt time.Time
}

// insertNotification enqueues a notification intent. It returns false when an
// identical intent already exists (dedupe key conflict), which is what keeps
// repeated scans from re-notifying the same offer.
func insertNotification(ctx context.Context, tx *sql.Tx, offerID int64, runID string, draft NotificationDraft, at time.Time) (bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO notifications
		(offer_id, run_id, kind, dedupe_key, payload, state, attempts, created_at, updated_at)
		VALUES (?,?,?,?,?,?,0,?,?)
		ON CONFLICT(dedupe_key) DO NOTHING`,
		offerID, runID, draft.Kind, draft.DedupeKey, draft.Payload,
		string(model.NotificationPending), at.UTC().Unix(), at.UTC().Unix())
	if err != nil {
		return false, fmt.Errorf("storage: insert notification: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("storage: notification rows affected: %w", err)
	}
	return affected > 0, nil
}

// PendingNotifications returns notification intents ready for delivery:
// pending rows plus failed rows that still have attempts left and whose retry
// time has arrived.
func (d *DB) PendingNotifications(ctx context.Context, now time.Time, maxAttempts, limit int) ([]Notification, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT id, offer_id, run_id, kind, dedupe_key, payload, state,
			attempts, COALESCE(last_error, ''), created_at
		FROM notifications
		WHERE state = ? OR (state = ? AND attempts < ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?))
		ORDER BY id LIMIT ?`,
		string(model.NotificationPending), string(model.NotificationFailed), maxAttempts, now.UTC().Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("storage: select pending notifications: %w", err)
	}
	defer rows.Close()

	var out []Notification
	for rows.Next() {
		var (
			n         Notification
			state     string
			createdAt int64
		)
		if err := rows.Scan(&n.ID, &n.OfferID, &n.RunID, &n.Kind, &n.DedupeKey, &n.Payload,
			&state, &n.Attempts, &n.LastError, &createdAt); err != nil {
			return nil, fmt.Errorf("storage: scan notification: %w", err)
		}
		n.State = model.NotificationState(state)
		n.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate notifications: %w", err)
	}
	return out, nil
}

// MarkNotificationSent records successful delivery.
func (d *DB) MarkNotificationSent(ctx context.Context, id int64, at time.Time) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE notifications
		SET state = ?, attempts = attempts + 1, sent_at = ?, updated_at = ?, last_error = NULL,
			next_attempt_at = NULL
		WHERE id = ?`,
		string(model.NotificationSent), at.UTC().Unix(), at.UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("storage: mark notification %d sent: %w", id, err)
	}
	return nil
}

// MarkNotificationFailed records a failed delivery attempt. When retryAt is
// non-nil the row becomes retryable after that time, otherwise it stays failed
// permanently (attempts exhausted).
func (d *DB) MarkNotificationFailed(ctx context.Context, id int64, attempts int, cause string, retryAt *time.Time, at time.Time) error {
	var next sql.NullInt64
	if retryAt != nil {
		next = sql.NullInt64{Int64: retryAt.UTC().Unix(), Valid: true}
	}
	_, err := d.sql.ExecContext(ctx, `UPDATE notifications
		SET state = ?, attempts = ?, last_error = ?, next_attempt_at = ?, updated_at = ?
		WHERE id = ?`,
		string(model.NotificationFailed), attempts, nullableString(cause), next, at.UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("storage: mark notification %d failed: %w", id, err)
	}
	return nil
}

// NotificationStats counts outbox rows by state.
func (d *DB) NotificationStats(ctx context.Context) (pending, sent, failed int, err error) {
	err = d.sql.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'sent' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0)
		FROM notifications`).Scan(&pending, &sent, &failed)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("storage: notification stats: %w", err)
	}
	return pending, sent, failed, nil
}

// GetNotification loads one outbox row by ID (tests and diagnostics).
func (d *DB) GetNotification(ctx context.Context, id int64) (Notification, bool, error) {
	var (
		n         Notification
		state     string
		createdAt int64
	)
	err := d.sql.QueryRowContext(ctx, `SELECT id, offer_id, run_id, kind, dedupe_key, payload, state,
			attempts, COALESCE(last_error, ''), created_at
		FROM notifications WHERE id = ?`, id).
		Scan(&n.ID, &n.OfferID, &n.RunID, &n.Kind, &n.DedupeKey, &n.Payload, &state,
			&n.Attempts, &n.LastError, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Notification{}, false, nil
	}
	if err != nil {
		return Notification{}, false, fmt.Errorf("storage: read notification: %w", err)
	}
	n.State = model.NotificationState(state)
	n.CreatedAt = time.Unix(createdAt, 0).UTC()
	return n, true, nil
}
