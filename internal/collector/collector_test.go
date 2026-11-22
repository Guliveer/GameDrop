package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Guliveer/GameDrop/internal/model"
	"github.com/Guliveer/GameDrop/internal/notify"
	"github.com/Guliveer/GameDrop/internal/storage"
	"github.com/Guliveer/GameDrop/internal/stores"
)

var cycleStart = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func openTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.Options{
		Path: filepath.Join(t.TempDir(), "gamedrop.db"),
	})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type fakeAdapter struct {
	store  model.Store
	offers []model.Offer
	err    error

	mu    sync.Mutex
	calls int
}

func (f *fakeAdapter) Store() model.Store { return f.store }

func (f *fakeAdapter) Fetch(ctx context.Context, q stores.Query) ([]model.Offer, error) {
	f.mu.Lock()
	f.calls++
	offers, err := f.offers, f.err
	f.mu.Unlock()

	out := make([]model.Offer, len(offers))
	copy(out, offers)
	return out, err
}

type fakeSender struct {
	mu    sync.Mutex
	texts []string
	err   error
}

func (f *fakeSender) Name() string { return "fake" }

func (f *fakeSender) Send(ctx context.Context, msg notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.texts = append(f.texts, msg.Text)
	return nil
}

func (f *fakeSender) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

func (f *fakeSender) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func testOffer(id string) model.Offer {
	return model.Offer{
		Store:         model.StoreEpic,
		OfferID:       id,
		Country:       "PL",
		Title:         "Game " + id,
		URL:           "https://store.example.test/" + id,
		Type:          model.TypeFreeToKeep,
		Availability:  model.AvailabilityAvailable,
		Price:         &model.Money{Currency: "PLN"},
		OriginalPrice: &model.Money{AmountMinor: 9999, Currency: "PLN"},
		EndsAt:        cycleStart.Add(72 * time.Hour),
	}
}

func newCollector(t *testing.T, db *storage.DB, adapters []stores.Adapter, sender notify.Sender, clock *time.Time) *Collector {
	t.Helper()
	return New(Options{
		DB:                db,
		Registry:          stores.NewRegistry(adapters...),
		Countries:         []string{"PL"},
		Locale:            "pl-PL",
		SourceTimeout:     5 * time.Second,
		MaxConcurrency:    2,
		Notifier:          sender,
		MaxNotifyAttempts: 3,
		Logger:            testLogger(),
		Now:               func() time.Time { return *clock },
	})
}

func TestCyclePersistsOffersAndNotifiesOnce(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := cycleStart

	adapter := &fakeAdapter{store: model.StoreEpic, offers: []model.Offer{testOffer("a")}}
	sender := &fakeSender{}
	c := newCollector(t, db, []stores.Adapter{adapter}, sender, &clock)

	summary, err := c.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Status != storage.RunStatusSucceeded {
		t.Errorf("status = %q", summary.Status)
	}
	if summary.OffersNew != 1 || summary.OffersSeen != 1 {
		t.Errorf("summary = %+v", summary)
	}
	if summary.NotificationsSent != 1 {
		t.Errorf("notifications sent = %d, want 1", summary.NotificationsSent)
	}
	if len(sender.sent()) != 1 || !strings.Contains(sender.sent()[0], "Game a") {
		t.Fatalf("sender = %v", sender.sent())
	}

	offers, err := db.ActiveOffers(ctx, clock)
	if err != nil {
		t.Fatalf("ActiveOffers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers = %d, want 1", len(offers))
	}

	// Second cycle: same data must not create a new offer or notification.
	clock = cycleStart.Add(time.Hour)
	summary, err = c.Run(ctx)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if summary.OffersNew != 0 || summary.NotificationsCreated != 0 {
		t.Errorf("second summary = %+v", summary)
	}
	if len(sender.sent()) != 1 {
		t.Errorf("sender received %d messages, want 1", len(sender.sent()))
	}

	offers, _ = db.ActiveOffers(ctx, clock)
	if len(offers) != 1 {
		t.Errorf("offers after second cycle = %d, want 1", len(offers))
	}
	history, err := db.OfferHistory(ctx, model.StoreEpic, "PL", "a")
	if err != nil {
		t.Fatalf("OfferHistory: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("history = %d entries, want 1", len(history))
	}

	run, found, err := db.GetRun(ctx, summary.RunID)
	if err != nil || !found {
		t.Fatalf("GetRun: found=%v err=%v", found, err)
	}
	if run.Status != storage.RunStatusSucceeded {
		t.Errorf("stored run status = %q", run.Status)
	}
}

func TestCycleIsolatesSourceFailures(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := cycleStart

	good := &fakeAdapter{store: model.StoreEpic, offers: []model.Offer{testOffer("a")}}
	bad := &fakeAdapter{store: model.StoreGOG, err: errors.New("gog is down")}

	summary, err := newCollector(t, db, []stores.Adapter{good, bad}, nil, &clock).Run(ctx)
	if err != nil {
		t.Fatalf("a partial run must not fail the cycle: %v", err)
	}
	if summary.Status != storage.RunStatusPartial {
		t.Errorf("status = %q, want partial", summary.Status)
	}
	if summary.SourcesOK != 1 || summary.SourcesFailed != 1 {
		t.Errorf("sources ok=%d failed=%d", summary.SourcesOK, summary.SourcesFailed)
	}
	if len(summary.Failures) != 1 {
		t.Errorf("failures = %v", summary.Failures)
	}

	state, found, err := db.GetSourceState(ctx, model.StoreGOG, "PL")
	if err != nil || !found {
		t.Fatalf("GetSourceState: found=%v err=%v", found, err)
	}
	if state.Status != model.SourceFailure {
		t.Errorf("gog state = %q", state.Status)
	}

	// A later run where the failing source is still down and the healthy one
	// reports nothing must preserve the stored offers.
	good.offers = nil
	good.err = nil
	clock = cycleStart.Add(time.Hour)
	if _, err := newCollector(t, db, []stores.Adapter{good, bad}, nil, &clock).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	offers, err := db.ActiveOffers(ctx, clock)
	if err != nil {
		t.Fatalf("ActiveOffers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers = %d, want the previously stored offer to survive", len(offers))
	}
}

func TestCycleFailsWhenEverySourceFails(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := cycleStart

	// Seed data that must survive the failed cycle.
	seed := &fakeAdapter{store: model.StoreEpic, offers: []model.Offer{testOffer("a")}}
	if _, err := newCollector(t, db, []stores.Adapter{seed}, nil, &clock).Run(ctx); err != nil {
		t.Fatalf("seed Run: %v", err)
	}

	failing := &fakeAdapter{store: model.StoreEpic, err: errors.New("epic is down")}
	summary, err := newCollector(t, db, []stores.Adapter{failing}, nil, &clock).Run(ctx)
	if !errors.Is(err, ErrNoSourceSucceeded) {
		t.Fatalf("err = %v, want ErrNoSourceSucceeded", err)
	}
	if summary.Status != storage.RunStatusFailed {
		t.Errorf("status = %q", summary.Status)
	}

	offers, err := db.ActiveOffers(ctx, clock)
	if err != nil {
		t.Fatalf("ActiveOffers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatal("a failed cycle must not erase stored offers")
	}

	run, _, err := db.GetRun(ctx, summary.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != storage.RunStatusFailed || run.Error == "" {
		t.Errorf("stored run = %+v", run)
	}
}

func TestFailedNotificationIsRetriedOnTheNextCycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := cycleStart

	adapter := &fakeAdapter{store: model.StoreEpic, offers: []model.Offer{testOffer("a")}}
	sender := &fakeSender{err: &notify.DeliveryError{Retryable: true, Err: errors.New("telegram timeout")}}
	c := newCollector(t, db, []stores.Adapter{adapter}, sender, &clock)

	summary, err := c.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.NotificationsSent != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	pending, sent, failed, err := db.NotificationStats(ctx)
	if err != nil {
		t.Fatalf("NotificationStats: %v", err)
	}
	if pending != 0 || sent != 0 || failed != 1 {
		t.Fatalf("outbox pending=%d sent=%d failed=%d, want the intent to be deferred", pending, sent, failed)
	}

	sender.setErr(nil)
	clock = cycleStart.Add(5 * time.Minute)
	summary, err = c.Run(ctx)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if summary.NotificationsSent != 1 {
		t.Fatalf("summary = %+v, want the deferred notification to be delivered", summary)
	}
	if len(sender.sent()) != 1 {
		t.Errorf("sender = %v", sender.sent())
	}
}

func TestUpcomingOfferNotifiesWhenItBecomesAvailable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := cycleStart

	upcoming := testOffer("soon")
	upcoming.Availability = model.AvailabilityUpcoming
	upcoming.StartsAt = cycleStart.Add(time.Hour)

	adapter := &fakeAdapter{store: model.StoreEpic, offers: []model.Offer{upcoming}}
	sender := &fakeSender{}
	c := newCollector(t, db, []stores.Adapter{adapter}, sender, &clock)

	if _, err := c.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sender.sent()) != 0 {
		t.Fatalf("an upcoming offer must not notify: %v", sender.sent())
	}

	available := upcoming
	available.Availability = model.AvailabilityAvailable
	adapter.offers = []model.Offer{available}

	clock = cycleStart.Add(2 * time.Hour)
	summary, err := c.Run(ctx)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if summary.NotificationsSent != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if !strings.Contains(sender.sent()[0], "Now available") {
		t.Errorf("message = %q", sender.sent()[0])
	}

	// A third identical cycle must not repeat it.
	clock = cycleStart.Add(3 * time.Hour)
	summary, err = c.Run(ctx)
	if err != nil {
		t.Fatalf("third Run: %v", err)
	}
	if summary.NotificationsCreated != 0 || len(sender.sent()) != 1 {
		t.Errorf("third summary = %+v, sent = %v", summary, sender.sent())
	}
}

func TestExpiredOfferIsRecordedWithoutNotification(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := cycleStart

	expired := testOffer("gone")
	expired.EndsAt = cycleStart.Add(-time.Hour)

	adapter := &fakeAdapter{store: model.StoreEpic, offers: []model.Offer{expired}}
	sender := &fakeSender{}
	c := newCollector(t, db, []stores.Adapter{adapter}, sender, &clock)

	summary, err := c.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.OffersExpired != 1 {
		t.Errorf("expired = %d, want 1", summary.OffersExpired)
	}
	if len(sender.sent()) != 0 {
		t.Errorf("expired offers must not notify: %v", sender.sent())
	}

	stored, found, err := db.GetOfferByKey(ctx, model.StoreEpic, "PL", "gone")
	if err != nil || !found {
		t.Fatalf("GetOfferByKey: found=%v err=%v", found, err)
	}
	if stored.Offer.Availability != model.AvailabilityExpired {
		t.Errorf("availability = %q", stored.Offer.Availability)
	}
}

func TestPartialSourcePersistsOffersWithoutMarkingMissing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := cycleStart

	stable := &fakeAdapter{store: model.StoreSteam, offers: []model.Offer{
		steamOffer("1001"), steamOffer("1002"),
	}}
	c := newCollector(t, db, []stores.Adapter{stable}, nil, &clock)
	if _, err := c.Run(ctx); err != nil {
		t.Fatalf("seed Run: %v", err)
	}

	// A partially successful scan returns one offer plus an error.
	partial := &fakeAdapter{store: model.StoreSteam, offers: []model.Offer{steamOffer("1001")}, err: errors.New("verification failed")}
	clock = cycleStart.Add(time.Hour)
	summary, err := newCollector(t, db, []stores.Adapter{partial}, nil, &clock).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.SourcesOK != 1 || summary.SourcesFailed != 0 {
		t.Errorf("summary = %+v", summary)
	}

	stored, found, err := db.GetOfferByKey(ctx, model.StoreSteam, "PL", "1002")
	if err != nil || !found {
		t.Fatalf("GetOfferByKey: found=%v err=%v", found, err)
	}
	if stored.MissingSince != nil {
		t.Error("a partial scan must not mark offers missing")
	}
}

func steamOffer(id string) model.Offer {
	return model.Offer{
		Store:         model.StoreSteam,
		OfferID:       id,
		Country:       "PL",
		Title:         "Steam " + id,
		Type:          model.TypeFreeToKeep,
		Availability:  model.AvailabilityAvailable,
		Price:         &model.Money{Currency: "PLN"},
		OriginalPrice: &model.Money{AmountMinor: 5999, Currency: "PLN"},
	}
}

func TestRunIDIsUniqueAndSortable(t *testing.T) {
	first := newRunID(cycleStart)
	second := newRunID(cycleStart)
	if first == second {
		t.Error("run IDs must differ")
	}
	if !strings.HasPrefix(first, "20261120T120000Z-") {
		t.Errorf("run ID = %q", first)
	}
}
