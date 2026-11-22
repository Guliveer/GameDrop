package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Guliveer/GameDrop/internal/model"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// openTestDB opens a temporary database whose clock the test controls.
func openTestDB(t *testing.T) (*DB, *time.Time) {
	t.Helper()
	clock := t0
	db, err := Open(context.Background(), Options{
		Path: filepath.Join(t.TempDir(), "gamedrop.db"),
		Now:  func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, &clock
}

func testOffer(id string) model.Offer {
	return model.Offer{
		Store:         model.StoreEpic,
		OfferID:       id,
		Country:       "PL",
		Title:         "Game " + id,
		Description:   "description",
		URL:           "https://store.example.test/" + id,
		ImageURL:      "https://cdn.example.test/" + id + ".jpg",
		Type:          model.TypeFreeToKeep,
		Availability:  model.AvailabilityAvailable,
		Price:         &model.Money{AmountMinor: 0, Currency: "PLN"},
		OriginalPrice: &model.Money{AmountMinor: 9999, Currency: "PLN"},
		EndsAt:        t0.Add(48 * time.Hour),
		Source:        "test",
	}
}

func scanInput(offers ...model.Offer) ScanInput {
	return ScanInput{RunID: "run-1", Store: model.StoreEpic, Country: "PL", Offers: offers, ObservedAt: t0, MarkMissing: true}
}

func TestMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "gamedrop.db")
	ctx := context.Background()

	db, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-opening an existing database must not re-run migrations or fail.
	db, err = Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer db.Close()
	if err := db.StartRun(ctx, "run-after-reopen", t0); err != nil {
		t.Fatalf("database unusable after re-open: %v", err)
	}
}

func TestApplyScanInsertUpdateUnchanged(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()

	res, err := db.ApplyScan(ctx, scanInput(testOffer("a")))
	if err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}
	if res.New != 1 || res.Seen != 1 {
		t.Fatalf("first scan = %+v", res)
	}

	stored, found, err := db.GetOfferByKey(ctx, model.StoreEpic, "PL", "a")
	if err != nil || !found {
		t.Fatalf("GetOfferByKey: found=%v err=%v", found, err)
	}
	if stored.Offer.Title != "Game a" {
		t.Errorf("title = %q", stored.Offer.Title)
	}
	if !stored.FirstSeenAt.Equal(t0) {
		t.Errorf("FirstSeenAt = %s, want %s", stored.FirstSeenAt, t0)
	}

	// The same data again is an idempotent no-op.
	*clock = t0.Add(time.Hour)
	secondScan := scanInput(testOffer("a"))
	secondScan.ObservedAt = *clock
	res, err = db.ApplyScan(ctx, secondScan)
	if err != nil {
		t.Fatalf("second ApplyScan: %v", err)
	}
	if res.Unchanged != 1 || res.New != 0 || res.Updated != 0 {
		t.Fatalf("second scan = %+v", res)
	}
	stored, _, _ = db.GetOfferByKey(ctx, model.StoreEpic, "PL", "a")
	if !stored.FirstSeenAt.Equal(t0) {
		t.Errorf("FirstSeenAt changed to %s", stored.FirstSeenAt)
	}
	if !stored.LastSeenAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("LastSeenAt = %s", stored.LastSeenAt)
	}

	// A real content change is reported as an update.
	*clock = t0.Add(2 * time.Hour)
	changed := testOffer("a")
	changed.Title = "Game a (renamed)"
	thirdScan := scanInput(changed)
	thirdScan.ObservedAt = *clock
	res, err = db.ApplyScan(ctx, thirdScan)
	if err != nil {
		t.Fatalf("third ApplyScan: %v", err)
	}
	if res.Updated != 1 {
		t.Fatalf("third scan = %+v", res)
	}

	history, err := db.OfferHistory(ctx, model.StoreEpic, "PL", "a")
	if err != nil {
		t.Fatalf("OfferHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history entries = %d, want 2 (created + updated)", len(history))
	}
	if history[0].Change != string(ChangeNew) || history[1].Change != string(ChangeUpdated) {
		t.Errorf("history changes = %q, %q", history[0].Change, history[1].Change)
	}
	if !strings.Contains(history[1].Snapshot, "renamed") {
		t.Errorf("history snapshot = %s", history[1].Snapshot)
	}
}

func TestApplyScanRejectsInvalidOffers(t *testing.T) {
	db, _ := openTestDB(t)
	ctx := context.Background()

	invalid := testOffer("broken")
	invalid.Title = "   "

	res, err := db.ApplyScan(ctx, scanInput(invalid))
	if err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}
	if res.Invalid != 1 || res.Seen != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Rejected) != 1 {
		t.Fatalf("rejected = %v", res.Rejected)
	}
	if _, found, _ := db.GetOfferByKey(ctx, model.StoreEpic, "PL", "broken"); found {
		t.Error("an invalid offer must not be persisted")
	}
}

func TestMissingOffersAreNeverDeletedOrExpired(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()

	if _, err := db.ApplyScan(ctx, scanInput(testOffer("a"), testOffer("b"))); err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}

	*clock = t0.Add(time.Hour)
	res, err := db.ApplyScan(ctx, scanInput(testOffer("a")))
	if err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}
	if res.Missing != 1 {
		t.Fatalf("missing = %d, want 1", res.Missing)
	}

	stored, found, _ := db.GetOfferByKey(ctx, model.StoreEpic, "PL", "b")
	if !found {
		t.Fatal("a missing offer must not be deleted")
	}
	if stored.Offer.Availability == model.AvailabilityExpired {
		t.Error("a missing offer must not be expired without a confirmed deadline")
	}
	if stored.MissingSince == nil {
		t.Error("MissingSince should be recorded")
	}
	if stored.MissedScans != 1 {
		t.Errorf("MissedScans = %d", stored.MissedScans)
	}

	// A source returning zero offers is inconclusive and must not mark
	// everything as missing.
	*clock = t0.Add(2 * time.Hour)
	empty := ScanInput{RunID: "run-2", Store: model.StoreEpic, Country: "PL", ObservedAt: *clock, MarkMissing: true}
	res, err = db.ApplyScan(ctx, empty)
	if err != nil {
		t.Fatalf("empty ApplyScan: %v", err)
	}
	if res.Missing != 0 {
		t.Errorf("empty scan marked %d offers missing, want 0", res.Missing)
	}

	// A partial scan must not mark anything missing either.
	*clock = t0.Add(3 * time.Hour)
	partial := scanInput(testOffer("a"))
	partial.MarkMissing = false
	res, err = db.ApplyScan(ctx, partial)
	if err != nil {
		t.Fatalf("partial ApplyScan: %v", err)
	}
	if res.Missing != 0 {
		t.Errorf("partial scan marked %d offers missing, want 0", res.Missing)
	}

	// Seeing the offer again clears the missing marker.
	*clock = t0.Add(4 * time.Hour)
	if _, err := db.ApplyScan(ctx, scanInput(testOffer("a"), testOffer("b"))); err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}
	stored, _, _ = db.GetOfferByKey(ctx, model.StoreEpic, "PL", "b")
	if stored.MissingSince != nil || stored.MissedScans != 0 {
		t.Errorf("missing marker not cleared: since=%v scans=%d", stored.MissingSince, stored.MissedScans)
	}
}

func TestExpireConfirmed(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()

	due := testOffer("due")
	due.EndsAt = t0.Add(time.Hour)
	noDeadline := testOffer("forever")
	noDeadline.EndsAt = time.Time{}

	if _, err := db.ApplyScan(ctx, scanInput(due, noDeadline)); err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}

	expired, err := db.ExpireConfirmed(ctx, "run-2", t0.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("ExpireConfirmed: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("expired %d offers before their deadline", len(expired))
	}

	*clock = t0.Add(2 * time.Hour)
	expired, err = db.ExpireConfirmed(ctx, "run-3", *clock)
	if err != nil {
		t.Fatalf("ExpireConfirmed: %v", err)
	}
	if len(expired) != 1 || expired[0].OfferID != "due" {
		t.Fatalf("expired = %+v", expired)
	}

	stored, _, _ := db.GetOfferByKey(ctx, model.StoreEpic, "PL", "due")
	if stored.Offer.Availability != model.AvailabilityExpired {
		t.Errorf("availability = %q", stored.Offer.Availability)
	}
	if stored.ExpiredAt == nil {
		t.Error("ExpiredAt must be recorded")
	}

	// Expiration is idempotent.
	again, err := db.ExpireConfirmed(ctx, "run-4", *clock)
	if err != nil {
		t.Fatalf("second ExpireConfirmed: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second call expired %d offers, want 0", len(again))
	}

	// An offer without a deadline never expires.
	stored, _, _ = db.GetOfferByKey(ctx, model.StoreEpic, "PL", "forever")
	if stored.Offer.Availability != model.AvailabilityAvailable {
		t.Errorf("offer without deadline availability = %q", stored.Offer.Availability)
	}
}

func TestReappearanceAfterExpiration(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()

	offer := testOffer("again")
	offer.EndsAt = t0.Add(time.Hour)
	if _, err := db.ApplyScan(ctx, scanInput(offer)); err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}

	*clock = t0.Add(2 * time.Hour)
	if _, err := db.ExpireConfirmed(ctx, "run-2", *clock); err != nil {
		t.Fatalf("ExpireConfirmed: %v", err)
	}

	*clock = t0.Add(3 * time.Hour)
	renewed := testOffer("again")
	renewed.EndsAt = t0.Add(72 * time.Hour)
	res, err := db.ApplyScan(ctx, scanInput(renewed))
	if err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}
	if res.Reappeared != 1 {
		t.Fatalf("result = %+v, want one reappearance", res)
	}

	stored, _, _ := db.GetOfferByKey(ctx, model.StoreEpic, "PL", "again")
	if stored.ExpiredAt != nil {
		t.Error("reappearance must clear ExpiredAt")
	}
	if stored.Offer.Availability != model.AvailabilityAvailable {
		t.Errorf("availability = %q", stored.Offer.Availability)
	}

	history, err := db.OfferHistory(ctx, model.StoreEpic, "PL", "again")
	if err != nil {
		t.Fatalf("OfferHistory: %v", err)
	}
	var sawReappear bool
	for _, entry := range history {
		if entry.Change == string(ChangeReappeared) {
			sawReappear = true
		}
	}
	if !sawReappear {
		t.Error("history must record the reappearance")
	}
}

func TestNotificationOutboxDedupeAndRetry(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()

	planner := func(offer model.Offer, previous *model.Offer, change Change, at time.Time) (NotificationDraft, bool) {
		return NotificationDraft{
			Kind:      "new",
			DedupeKey: "new:" + offer.Key(),
			Payload:   "payload for " + offer.Title,
		}, true
	}

	in := scanInput(testOffer("a"))
	in.Planner = planner
	res, err := db.ApplyScan(ctx, in)
	if err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}
	if res.NotificationsCreated != 1 {
		t.Fatalf("created = %d, want 1", res.NotificationsCreated)
	}

	// A repeated scan must not enqueue the same intent again.
	*clock = t0.Add(time.Minute)
	in = scanInput(testOffer("a"))
	in.Planner = planner
	in.ObservedAt = *clock
	res, err = db.ApplyScan(ctx, in)
	if err != nil {
		t.Fatalf("second ApplyScan: %v", err)
	}
	if res.NotificationsCreated != 0 {
		t.Fatalf("duplicate intent created: %+v", res)
	}

	pending, err := db.PendingNotifications(ctx, *clock, 3, 10)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if pending[0].Payload != "payload for Game a" {
		t.Errorf("payload = %q", pending[0].Payload)
	}

	// A transient failure is retried only after its backoff time.
	retryAt := clock.Add(5 * time.Minute)
	if err := db.MarkNotificationFailed(ctx, pending[0].ID, 1, "temporary", &retryAt, *clock); err != nil {
		t.Fatalf("MarkNotificationFailed: %v", err)
	}
	stillPending, err := db.PendingNotifications(ctx, *clock, 3, 10)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if len(stillPending) != 0 {
		t.Fatalf("failed notification should wait for its retry time, got %d", len(stillPending))
	}

	*clock = t0.Add(10 * time.Minute)
	due, err := db.PendingNotifications(ctx, *clock, 3, 10)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("due = %+v, want one retryable notification", due)
	}

	// Exhausted attempts are no longer returned.
	_, err = db.PendingNotifications(ctx, *clock, 1, 10)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	due, _ = db.PendingNotifications(ctx, *clock, 2, 10)
	if len(due) != 1 {
		t.Fatalf("expected the notification to remain retryable, got %d", len(due))
	}

	if err := db.MarkNotificationSent(ctx, pending[0].ID, *clock); err != nil {
		t.Fatalf("MarkNotificationSent: %v", err)
	}
	due, _ = db.PendingNotifications(ctx, *clock, 3, 10)
	if len(due) != 0 {
		t.Errorf("sent notification still pending: %+v", due)
	}
	notif, found, err := db.GetNotification(ctx, pending[0].ID)
	if err != nil || !found {
		t.Fatalf("GetNotification: found=%v err=%v", found, err)
	}
	if notif.State != model.NotificationSent {
		t.Errorf("state = %q", notif.State)
	}
	if notif.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", notif.Attempts)
	}
}

func TestApplyScanRollsBackOnFailure(t *testing.T) {
	db, _ := openTestDB(t)
	ctx := context.Background()

	// Force the notification insert to fail so the whole scan transaction has
	// to roll back, including the offer that was inserted before it.
	if _, err := db.sql.ExecContext(ctx, `CREATE TRIGGER reject_notifications
		BEFORE INSERT ON notifications BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	in := scanInput(testOffer("rollback"))
	in.Planner = func(offer model.Offer, previous *model.Offer, change Change, at time.Time) (NotificationDraft, bool) {
		return NotificationDraft{Kind: "new", DedupeKey: "new:" + offer.Key(), Payload: "payload"}, true
	}

	if _, err := db.ApplyScan(ctx, in); err == nil {
		t.Fatal("expected the scan to fail")
	}
	if _, found, _ := db.GetOfferByKey(ctx, model.StoreEpic, "PL", "rollback"); found {
		t.Fatal("a failed scan must not leave a partially written offer")
	}
}

func TestSourceStateAndRuns(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()

	if err := db.StartRun(ctx, "run-1", *clock); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := db.RecordSourceRun(ctx, SourceRun{
		RunID:       "run-1",
		Source:      model.StoreEpic,
		Country:     "pl",
		Status:      model.SourceFailure,
		OffersFound: 0,
		Duration:    1500 * time.Millisecond,
		Error:       "boom",
		StartedAt:   *clock,
		FinishedAt:  clock.Add(1500 * time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordSourceRun: %v", err)
	}

	state, found, err := db.GetSourceState(ctx, model.StoreEpic, "PL")
	if err != nil || !found {
		t.Fatalf("GetSourceState: found=%v err=%v", found, err)
	}
	if state.Status != model.SourceFailure || state.LastError != "boom" {
		t.Errorf("state = %+v", state)
	}
	if state.LastSuccessAt != nil {
		t.Error("a failure must not set LastSuccessAt")
	}

	// A later success keeps the failure history but updates the success time.
	*clock = t0.Add(time.Hour)
	if err := db.RecordSourceRun(ctx, SourceRun{
		RunID:      "run-1",
		Source:     model.StoreEpic,
		Country:    "PL",
		Status:     model.SourceSuccess,
		StartedAt:  *clock,
		FinishedAt: clock.Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordSourceRun: %v", err)
	}
	state, _, _ = db.GetSourceState(ctx, model.StoreEpic, "PL")
	if state.Status != model.SourceSuccess || state.LastSuccessAt == nil {
		t.Errorf("state = %+v", state)
	}

	if err := db.FinishRun(ctx, RunSummary{
		ID: "run-1", Status: RunStatusPartial, SourcesOK: 2, SourcesFailed: 1,
		OffersSeen: 5, OffersNew: 2, OffersChanged: 1, OffersExpired: 0,
		NotificationsSent: 1, NotificationsFailed: 0,
	}, clock.Add(2*time.Second)); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	run, found, err := db.GetRun(ctx, "run-1")
	if err != nil || !found {
		t.Fatalf("GetRun: found=%v err=%v", found, err)
	}
	if run.Status != RunStatusPartial || run.OffersSeen != 5 || run.SourcesFailed != 1 {
		t.Errorf("run = %+v", run)
	}
	if run.FinishedAt == nil {
		t.Error("FinishedAt missing")
	}

	sourceRuns, err := db.SourceRuns(ctx, "run-1")
	if err != nil {
		t.Fatalf("SourceRuns: %v", err)
	}
	if len(sourceRuns) != 2 {
		t.Fatalf("source runs = %d, want 2", len(sourceRuns))
	}
}

func TestStats(t *testing.T) {
	db, clock := openTestDB(t)
	ctx := context.Background()

	available := testOffer("available")
	expired := testOffer("expired")
	expired.EndsAt = t0.Add(-time.Hour)
	if _, err := db.ApplyScan(ctx, scanInput(available, expired)); err != nil {
		t.Fatalf("ApplyScan: %v", err)
	}
	if _, err := db.ExpireConfirmed(ctx, "run-1", *clock); err != nil {
		t.Fatalf("ExpireConfirmed: %v", err)
	}

	stats, err := db.Stats(ctx, *clock)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.OffersTotal != 2 {
		t.Errorf("OffersTotal = %d", stats.OffersTotal)
	}
	if stats.OffersAvailable != 1 {
		t.Errorf("OffersAvailable = %d", stats.OffersAvailable)
	}
	if stats.OffersExpired != 1 {
		t.Errorf("OffersExpired = %d", stats.OffersExpired)
	}
}
