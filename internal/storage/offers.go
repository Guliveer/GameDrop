package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Guliveer/GameDrop/internal/model"
)

// Change describes what happened to an offer during a scan.
type Change string

// Offer change kinds recorded in history and used for notification decisions.
const (
	ChangeNew        Change = "new"
	ChangeUpdated    Change = "updated"
	ChangeUnchanged  Change = "unchanged"
	ChangeReappeared Change = "reappeared"
	ChangeExpired    Change = "expired"
)

// StoredOffer is an offer plus its persistence metadata.
type StoredOffer struct {
	ID          int64
	Offer       model.Offer
	Fingerprint string

	FirstSeenAt    time.Time
	LastSeenAt     time.Time
	LastVerifiedAt *time.Time
	MissingSince   *time.Time
	MissedScans    int
	ExpiredAt      *time.Time
}

// NotificationDraft is a notification intent produced by a Planner.
type NotificationDraft struct {
	// Kind is a short event name, for example "new" or "now_available".
	Kind string
	// DedupeKey uniquely identifies this notification across runs.
	DedupeKey string
	// Payload is the rendered message text, stored before delivery so intent
	// survives a Telegram outage.
	Payload string
}

// Planner decides whether an offer change deserves a notification and renders
// it. It runs inside the scan transaction.
//
// previous is nil for a first-time offer.
type Planner func(offer model.Offer, previous *model.Offer, change Change, at time.Time) (NotificationDraft, bool)

// ScanInput describes one successful (or partially successful) source scan for
// a single store and country.
type ScanInput struct {
	RunID      string
	Store      model.Store
	Country    string
	Offers     []model.Offer
	ObservedAt time.Time
	// MarkMissing flags offers of this store/country that were absent from the
	// scan. It must only be set for fully successful scans.
	MarkMissing bool
	// Planner is optional. When nil no notifications are created.
	Planner Planner
}

// ScanResult summarizes an applied scan.
type ScanResult struct {
	Store   model.Store
	Country string

	Seen       int
	New        int
	Updated    int
	Unchanged  int
	Reappeared int
	Expired    int
	// Missing counts offers previously known for this store/country that were
	// absent from this scan. Missing offers are never deleted or expired.
	Missing int
	// Invalid counts adapter offers rejected by validation.
	Invalid int
	// Rejected holds validation failures for logging.
	Rejected []string
	// NotificationsCreated counts newly enqueued notification intents.
	NotificationsCreated int
}

// ApplyScan persists the offers observed in one scan inside a single
// transaction: upserts, history entries, notification intents, and
// missing-marking.
//
// An offer absent from a successful scan is marked missing (never expired or
// deleted). Expiration requires a confirmed end date and is handled by
// ExpireConfirmed.
func (d *DB) ApplyScan(ctx context.Context, in ScanInput) (ScanResult, error) {
	res := ScanResult{Store: in.Store, Country: normalizeCountry(in.Country)}
	at := in.ObservedAt.UTC()
	if in.ObservedAt.IsZero() {
		at = d.now().UTC()
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("storage: begin scan transaction: %w", err)
	}
	defer tx.Rollback()

	seenIDs := make([]string, 0, len(in.Offers))
	for _, offer := range in.Offers {
		offer.Country = res.Country
		offer.Normalize()
		if err := offer.Validate(); err != nil {
			res.Invalid++
			res.Rejected = append(res.Rejected, err.Error())
			continue
		}

		change, previous, offerID, err := upsertOffer(ctx, tx, in.RunID, offer, at)
		if err != nil {
			return res, err
		}
		seenIDs = append(seenIDs, offer.OfferID)
		res.Seen++
		switch change {
		case ChangeNew:
			res.New++
		case ChangeUpdated:
			res.Updated++
		case ChangeReappeared:
			res.Reappeared++
		default:
			res.Unchanged++
		}

		if in.Planner == nil {
			continue
		}
		draft, ok := in.Planner(offer, previous, change, at)
		if !ok {
			continue
		}
		created, err := insertNotification(ctx, tx, offerID, in.RunID, draft, at)
		if err != nil {
			return res, err
		}
		if created {
			res.NotificationsCreated++
		}
	}

	// A source returning zero offers is treated as inconclusive: previous data
	// is kept untouched rather than marked missing. Partial scans likewise never
	// mark anything missing, because the adapter itself reported missing data.
	if in.MarkMissing && len(seenIDs) > 0 {
		missing, err := markMissing(ctx, tx, in.Store, res.Country, seenIDs, at)
		if err != nil {
			return res, err
		}
		res.Missing = missing
	}

	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("storage: commit scan: %w", err)
	}
	return res, nil
}

// upsertOffer inserts or updates one offer, records history for meaningful
// changes, and returns the change kind and the previously stored offer.
func upsertOffer(ctx context.Context, tx *sql.Tx, runID string, offer model.Offer, at time.Time) (Change, *model.Offer, int64, error) {
	stored, err := queryOffer(ctx, tx,
		`SELECT `+offerColumns+` FROM offers WHERE store = ? AND country = ? AND store_offer_id = ?`,
		string(offer.Store), offer.Country, offer.OfferID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return insertOffer(ctx, tx, runID, offer, at)
	case err != nil:
		return ChangeUnchanged, nil, 0, err
	}

	previous := stored.Offer
	fingerprint := offer.Fingerprint()
	reappeared := stored.ExpiredAt != nil && offer.Availability == model.AvailabilityAvailable && !offer.IsExpiredAt(at)

	if stored.Fingerprint == fingerprint && !reappeared {
		if _, err := tx.ExecContext(ctx,
			`UPDATE offers SET last_seen_at = ?, last_verified_at = ?, missing_since = NULL, missed_scans = 0 WHERE id = ?`,
			at.Unix(), at.Unix(), stored.ID); err != nil {
			return ChangeUnchanged, &previous, stored.ID, fmt.Errorf("storage: refresh offer %d: %w", stored.ID, err)
		}
		return ChangeUnchanged, &previous, stored.ID, nil
	}

	expiredAt := timeToNull(timeFromNull(sqlNullTime(stored.ExpiredAt)))
	if reappeared {
		expiredAt = sql.NullInt64{}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE offers SET
			title = ?, description = ?, url = ?, image_url = ?, offer_type = ?, availability = ?,
			price_minor = ?, price_currency = ?, original_minor = ?, original_currency = ?,
			starts_at = ?, ends_at = ?, source = ?, fingerprint = ?,
			last_seen_at = ?, last_verified_at = ?, missing_since = NULL, missed_scans = 0, expired_at = ?
		WHERE id = ?`,
		offer.Title, offer.Description, offer.URL, offer.ImageURL, string(offer.Type), string(offer.Availability),
		priceAmount(offer.Price), priceCurrency(offer.Price), priceAmount(offer.OriginalPrice), priceCurrency(offer.OriginalPrice),
		timeToNull(offer.StartsAt), timeToNull(offer.EndsAt), offer.Source, fingerprint,
		at.Unix(), at.Unix(), expiredAt, stored.ID); err != nil {
		return ChangeUnchanged, &previous, stored.ID, fmt.Errorf("storage: update offer %d: %w", stored.ID, err)
	}

	change := ChangeUpdated
	if reappeared {
		change = ChangeReappeared
	}
	if err := recordHistory(ctx, tx, stored.ID, runID, change, offer, at); err != nil {
		return change, &previous, stored.ID, err
	}
	return change, &previous, stored.ID, nil
}

// insertOffer creates a new offer row plus its initial history entry.
func insertOffer(ctx context.Context, tx *sql.Tx, runID string, offer model.Offer, at time.Time) (Change, *model.Offer, int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO offers (
			store, country, store_offer_id, title, description, url, image_url, offer_type, availability,
			price_minor, price_currency, original_minor, original_currency, starts_at, ends_at, source, fingerprint,
			first_seen_at, last_seen_at, last_verified_at, missing_since, missed_scans, expired_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,0,NULL)`,
		string(offer.Store), offer.Country, offer.OfferID, offer.Title, offer.Description, offer.URL, offer.ImageURL,
		string(offer.Type), string(offer.Availability),
		priceAmount(offer.Price), priceCurrency(offer.Price), priceAmount(offer.OriginalPrice), priceCurrency(offer.OriginalPrice),
		timeToNull(offer.StartsAt), timeToNull(offer.EndsAt), offer.Source, offer.Fingerprint(),
		at.Unix(), at.Unix(), at.Unix())
	if err != nil {
		return ChangeUnchanged, nil, 0, fmt.Errorf("storage: insert offer %s: %w", offer.Key(), err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ChangeUnchanged, nil, 0, fmt.Errorf("storage: offer id: %w", err)
	}
	if err := recordHistory(ctx, tx, id, runID, ChangeNew, offer, at); err != nil {
		return ChangeNew, nil, id, err
	}
	return ChangeNew, nil, id, nil
}

// markMissing flags offers of a store/country that were absent from a
// successful scan. This is informational only: nothing is deleted or expired.
func markMissing(ctx context.Context, tx *sql.Tx, store model.Store, country string, seenIDs []string, at time.Time) (int, error) {
	query := `UPDATE offers SET missing_since = COALESCE(missing_since, ?), missed_scans = missed_scans + 1
		WHERE store = ? AND country = ?`
	args := []any{at.Unix(), string(store), country}

	placeholders := make([]byte, 0, len(seenIDs)*2)
	for i := range seenIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
	}
	query += ` AND store_offer_id NOT IN (` + string(placeholders) + `)`
	for _, id := range seenIDs {
		args = append(args, id)
	}

	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("storage: mark missing offers: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: missing rows affected: %w", err)
	}
	return int(affected), nil
}

// ExpireConfirmed marks offers expired only when a confirmed end date has
// passed. It returns the offers that were newly expired.
func (d *DB) ExpireConfirmed(ctx context.Context, runID string, now time.Time) ([]model.Offer, error) {
	now = now.UTC()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: begin expire transaction: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT `+offerColumns+` FROM offers
		WHERE ends_at IS NOT NULL AND ends_at <= ? AND availability != ?`,
		now.Unix(), string(model.AvailabilityExpired))
	if err != nil {
		return nil, fmt.Errorf("storage: select expired offers: %w", err)
	}
	var due []StoredOffer
	for rows.Next() {
		stored, err := scanOffer(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("storage: scan expired offer: %w", err)
		}
		due = append(due, stored)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("storage: iterate expired offers: %w", err)
	}
	rows.Close()

	expired := make([]model.Offer, 0, len(due))
	for _, stored := range due {
		if _, err := tx.ExecContext(ctx,
			`UPDATE offers SET availability = ?, expired_at = ? WHERE id = ?`,
			string(model.AvailabilityExpired), now.Unix(), stored.ID); err != nil {
			return nil, fmt.Errorf("storage: mark offer %d expired: %w", stored.ID, err)
		}
		offer := stored.Offer
		offer.Availability = model.AvailabilityExpired
		if err := recordHistory(ctx, tx, stored.ID, runID, ChangeExpired, offer, now); err != nil {
			return nil, err
		}
		expired = append(expired, offer)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("storage: commit expiration: %w", err)
	}
	return expired, nil
}

// GetOfferByKey loads one offer by its canonical identity.
func (d *DB) GetOfferByKey(ctx context.Context, store model.Store, country, offerID string) (StoredOffer, bool, error) {
	stored, err := queryOffer(ctx, d.sql,
		`SELECT `+offerColumns+` FROM offers WHERE store = ? AND country = ? AND store_offer_id = ?`,
		string(store), normalizeCountry(country), offerID)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredOffer{}, false, nil
	}
	if err != nil {
		return StoredOffer{}, false, err
	}
	return stored, true, nil
}

// ActiveOffers returns offers that are currently available in every country,
// newest deadline first.
func (d *DB) ActiveOffers(ctx context.Context, now time.Time) ([]StoredOffer, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+offerColumns+` FROM offers
		WHERE availability IN (?, ?) AND (ends_at IS NULL OR ends_at > ?)
		ORDER BY COALESCE(ends_at, 0), store, country, store_offer_id`,
		string(model.AvailabilityAvailable), string(model.AvailabilityUpcoming), now.UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("storage: select active offers: %w", err)
	}
	defer rows.Close()

	var out []StoredOffer
	for rows.Next() {
		stored, err := scanOffer(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan active offer: %w", err)
		}
		out = append(out, stored)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate active offers: %w", err)
	}
	return out, nil
}

// Stats holds aggregate counters used for the run summary.
type Stats struct {
	OffersTotal          int
	OffersAvailable      int
	OffersUpcoming       int
	OffersExpired        int
	OffersMissing        int
	NotificationsPending int
	NotificationsSent    int
	NotificationsFailed  int
}

// Stats computes aggregate counters.
func (d *DB) Stats(ctx context.Context, now time.Time) (Stats, error) {
	var s Stats
	err := d.sql.QueryRowContext(ctx, `SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN availability = 'available' AND (ends_at IS NULL OR ends_at > ?) THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN availability = 'upcoming' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN availability = 'expired' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN missing_since IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM offers`, now.UTC().Unix()).
		Scan(&s.OffersTotal, &s.OffersAvailable, &s.OffersUpcoming, &s.OffersExpired, &s.OffersMissing)
	if err != nil {
		return s, fmt.Errorf("storage: offer stats: %w", err)
	}
	err = d.sql.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'sent' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0)
		FROM notifications`).Scan(&s.NotificationsPending, &s.NotificationsSent, &s.NotificationsFailed)
	if err != nil {
		return s, fmt.Errorf("storage: notification stats: %w", err)
	}
	return s, nil
}

// queryOffer runs a single-row offer query.
func queryOffer(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query string, args ...any) (StoredOffer, error) {
	return scanOffer(q.QueryRowContext(ctx, query, args...))
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOffer(row scanner) (StoredOffer, error) {
	var (
		stored                            StoredOffer
		store, country, offerID           string
		title, description, url, imageURL string
		offerType, availability, source   string
		priceCurrency, originalCurrency   string
		priceMinor, originalMinor         sql.NullInt64
		startsAt, endsAt, lastVerified    sql.NullInt64
		missingSince, expiredAt           sql.NullInt64
		firstSeen, lastSeen               int64
		missedScans                       int64
	)
	if err := row.Scan(
		&stored.ID, &store, &country, &offerID, &title, &description, &url, &imageURL,
		&offerType, &availability, &priceMinor, &priceCurrency, &originalMinor, &originalCurrency,
		&startsAt, &endsAt, &source, &stored.Fingerprint, &firstSeen, &lastSeen, &lastVerified,
		&missingSince, &missedScans, &expiredAt,
	); err != nil {
		return StoredOffer{}, err
	}

	stored.Offer = model.Offer{
		Store:        model.ParseStore(store),
		OfferID:      offerID,
		Country:      country,
		Title:        title,
		Description:  description,
		URL:          url,
		ImageURL:     imageURL,
		Type:         model.ParseOfferType(offerType),
		Availability: model.ParseAvailability(availability),
		StartsAt:     timeFromNull(startsAt),
		EndsAt:       timeFromNull(endsAt),
		Source:       source,
	}
	if priceMinor.Valid {
		stored.Offer.Price = &model.Money{AmountMinor: priceMinor.Int64, Currency: priceCurrency}
	}
	if originalMinor.Valid {
		stored.Offer.OriginalPrice = &model.Money{AmountMinor: originalMinor.Int64, Currency: originalCurrency}
	}
	stored.FirstSeenAt = timeFromNull(sql.NullInt64{Int64: firstSeen, Valid: true})
	stored.LastSeenAt = timeFromNull(sql.NullInt64{Int64: lastSeen, Valid: true})
	stored.LastVerifiedAt = timePtr(lastVerified)
	stored.MissingSince = timePtr(missingSince)
	stored.MissedScans = int(missedScans)
	stored.ExpiredAt = timePtr(expiredAt)
	return stored, nil
}

// offerSnapshot is the JSON shape written to offer_history.
type offerSnapshot struct {
	Store         string `json:"store"`
	OfferID       string `json:"offer_id"`
	Country       string `json:"country"`
	Title         string `json:"title"`
	URL           string `json:"url"`
	Type          string `json:"type"`
	Availability  string `json:"availability"`
	Price         string `json:"price,omitempty"`
	OriginalPrice string `json:"original_price,omitempty"`
	StartsAt      string `json:"starts_at,omitempty"`
	EndsAt        string `json:"ends_at,omitempty"`
	Source        string `json:"source,omitempty"`
}

func recordHistory(ctx context.Context, tx *sql.Tx, offerID int64, runID string, change Change, offer model.Offer, at time.Time) error {
	snap := offerSnapshot{
		Store:        string(offer.Store),
		OfferID:      offer.OfferID,
		Country:      offer.Country,
		Title:        offer.Title,
		URL:          offer.URL,
		Type:         string(offer.Type),
		Availability: string(offer.Availability),
		Source:       offer.Source,
	}
	if offer.Price != nil {
		snap.Price = offer.Price.String()
	}
	if offer.OriginalPrice != nil {
		snap.OriginalPrice = offer.OriginalPrice.String()
	}
	if !offer.StartsAt.IsZero() {
		snap.StartsAt = offer.StartsAt.Format(time.RFC3339)
	}
	if !offer.EndsAt.IsZero() {
		snap.EndsAt = offer.EndsAt.Format(time.RFC3339)
	}
	encoded, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("storage: encode offer snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO offer_history (offer_id, run_id, change, snapshot, recorded_at) VALUES (?,?,?,?,?)`,
		offerID, runID, string(change), string(encoded), at.Unix()); err != nil {
		return fmt.Errorf("storage: insert offer history: %w", err)
	}
	return nil
}

// OfferHistory returns the history entries for one offer, oldest first.
func (d *DB) OfferHistory(ctx context.Context, store model.Store, country, offerID string) ([]HistoryEntry, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT h.run_id, h.change, h.snapshot, h.recorded_at
		FROM offer_history h JOIN offers o ON o.id = h.offer_id
		WHERE o.store = ? AND o.country = ? AND o.store_offer_id = ?
		ORDER BY h.id`, string(store), normalizeCountry(country), offerID)
	if err != nil {
		return nil, fmt.Errorf("storage: select offer history: %w", err)
	}
	defer rows.Close()

	var out []HistoryEntry
	for rows.Next() {
		var entry HistoryEntry
		var recordedAt int64
		if err := rows.Scan(&entry.RunID, &entry.Change, &entry.Snapshot, &recordedAt); err != nil {
			return nil, fmt.Errorf("storage: scan offer history: %w", err)
		}
		entry.RecordedAt = time.Unix(recordedAt, 0).UTC()
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate offer history: %w", err)
	}
	return out, nil
}

// HistoryEntry is one offer history row.
type HistoryEntry struct {
	RunID      string
	Change     string
	Snapshot   string
	RecordedAt time.Time
}

func priceAmount(m *model.Money) sql.NullInt64 {
	if m == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: m.AmountMinor, Valid: true}
}

func priceCurrency(m *model.Money) string {
	if m == nil {
		return ""
	}
	return m.Currency
}

func sqlNullTime(t *time.Time) sql.NullInt64 {
	if t == nil || t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

func normalizeCountry(country string) string {
	out := make([]rune, 0, len(country))
	for _, r := range country {
		if r == ' ' {
			continue
		}
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		out = append(out, r)
	}
	return string(out)
}
