// Package collector orchestrates one collection cycle: fetch from every enabled
// store and country with bounded concurrency, persist offers atomically,
// expire offers only on confirmed evidence, and drain the notification outbox.
package collector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Guliveer/GameDrop/internal/model"
	"github.com/Guliveer/GameDrop/internal/notify"
	"github.com/Guliveer/GameDrop/internal/storage"
	"github.com/Guliveer/GameDrop/internal/stores"
)

// ErrNoSourceSucceeded means every configured source failed. Persisted data is
// left untouched, but the run must be visible as a failure.
var ErrNoSourceSucceeded = errors.New("collector: no configured source succeeded")

// Options configures a Collector.
type Options struct {
	DB             *storage.DB
	Registry       *stores.Registry
	Countries      []string
	Locale         string
	SourceTimeout  time.Duration
	MaxConcurrency int
	Logger         *slog.Logger

	// Notifier is nil when notifications are disabled. When nil, no
	// notification intents are created.
	Notifier          notify.Sender
	MaxNotifyAttempts int

	// Now is the clock, overridable in tests.
	Now func() time.Time
}

// Collector runs collection cycles.
type Collector struct {
	opts Options
}

// New builds a Collector.
func New(opts Options) *Collector {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxConcurrency < 1 {
		opts.MaxConcurrency = 1
	}
	if opts.SourceTimeout <= 0 {
		opts.SourceTimeout = 45 * time.Second
	}
	return &Collector{opts: opts}
}

// Summary is the outcome of one collection cycle.
type Summary struct {
	RunID         string
	StartedAt     time.Time
	FinishedAt    time.Time
	Status        string
	SourcesTotal  int
	SourcesOK     int
	SourcesFailed int

	OffersSeen           int
	OffersNew            int
	OffersChanged        int
	OffersExpired        int
	OffersMissing        int
	InvalidOffers        int
	NotificationsCreated int
	NotificationsSent    int
	NotificationsFailed  int

	Failures []string
}

// Duration returns the cycle duration.
func (s Summary) Duration() time.Duration { return s.FinishedAt.Sub(s.StartedAt) }

// Run executes one collection cycle.
func (c *Collector) Run(ctx context.Context) (Summary, error) {
	started := c.opts.Now().UTC()
	runID := newRunID(started)
	log := c.opts.Logger.With("run_id", runID)
	sum := Summary{RunID: runID, StartedAt: started}

	if err := c.opts.DB.StartRun(ctx, runID, started); err != nil {
		return sum, err
	}

	log.InfoContext(ctx, "collection cycle started",
		"countries", c.opts.Countries,
		"locale", c.opts.Locale,
		"sources", c.opts.Registry.Len(),
	)

	results := c.fetchAll(ctx)
	for _, result := range results {
		c.persist(ctx, log, runID, &sum, result)
	}

	now := c.opts.Now().UTC()
	expired, err := c.opts.DB.ExpireConfirmed(ctx, runID, now)
	if err != nil {
		return c.finish(ctx, log, sum, err)
	}
	sum.OffersExpired = len(expired)
	for _, offer := range expired {
		log.InfoContext(ctx, "offer expired", "store", offer.Store,
			"country", offer.Country, "offer_id", offer.OfferID, "title", offer.Title)
	}

	if c.opts.Notifier != nil {
		processor := &notify.Processor{
			DB:          c.opts.DB,
			Sender:      c.opts.Notifier,
			Logger:      log,
			MaxAttempts: c.opts.MaxNotifyAttempts,
			Now:         c.opts.Now,
		}
		report, err := processor.Run(ctx)
		sum.NotificationsSent += report.Sent
		sum.NotificationsFailed += report.Failed
		if err != nil {
			log.ErrorContext(ctx, "notification outbox failed", "error", err)
		}
	}

	var runErr error
	switch {
	case sum.SourcesOK == 0 && sum.SourcesFailed > 0:
		sum.Status = storage.RunStatusFailed
		runErr = ErrNoSourceSucceeded
	case sum.SourcesFailed > 0:
		sum.Status = storage.RunStatusPartial
	default:
		sum.Status = storage.RunStatusSucceeded
	}

	return c.finish(ctx, log, sum, runErr)
}

// finish records the run result and emits the execution summary.
func (c *Collector) finish(ctx context.Context, log *slog.Logger, sum Summary, runErr error) (Summary, error) {
	sum.FinishedAt = c.opts.Now().UTC()

	summary := storage.RunSummary{
		ID:                  sum.RunID,
		Status:              sum.Status,
		SourcesOK:           sum.SourcesOK,
		SourcesFailed:       sum.SourcesFailed,
		OffersSeen:          sum.OffersSeen,
		OffersNew:           sum.OffersNew,
		OffersChanged:       sum.OffersChanged,
		OffersExpired:       sum.OffersExpired,
		NotificationsSent:   sum.NotificationsSent,
		NotificationsFailed: sum.NotificationsFailed,
	}
	if runErr != nil {
		summary.Error = runErr.Error()
	}
	if err := c.opts.DB.FinishRun(ctx, summary, sum.FinishedAt); err != nil {
		log.ErrorContext(ctx, "cannot record run summary", "error", err)
		if runErr == nil {
			runErr = err
		}
	}

	log.InfoContext(ctx, "collection cycle finished",
		"status", sum.Status,
		"duration_ms", sum.Duration().Milliseconds(),
		"sources_total", sum.SourcesTotal,
		"sources_ok", sum.SourcesOK,
		"sources_failed", sum.SourcesFailed,
		"offers_seen", sum.OffersSeen,
		"offers_new", sum.OffersNew,
		"offers_changed", sum.OffersChanged,
		"offers_expired", sum.OffersExpired,
		"offers_missing", sum.OffersMissing,
		"offers_invalid", sum.InvalidOffers,
		"notifications_created", sum.NotificationsCreated,
		"notifications_sent", sum.NotificationsSent,
		"notifications_failed", sum.NotificationsFailed,
	)
	return sum, runErr
}

// persist records one adapter result and applies its scan.
func (c *Collector) persist(ctx context.Context, log *slog.Logger, runID string, sum *Summary, result fetchResult) {
	sum.SourcesTotal++
	if result.status.OK() {
		sum.SourcesOK++
	} else {
		sum.SourcesFailed++
		if result.err != nil {
			sum.Failures = append(sum.Failures, fmt.Sprintf("%s/%s: %v", result.store, result.country, result.err))
			log.WarnContext(ctx, "source failed", "source", result.store, "country", result.country,
				"status", result.status, "duration_ms", result.duration.Milliseconds(), "error", result.err)
		}
	}

	if err := c.opts.DB.RecordSourceRun(ctx, storage.SourceRun{
		RunID:       runID,
		Source:      result.store,
		Country:     result.country,
		Status:      result.status,
		OffersFound: len(result.offers),
		Duration:    result.duration,
		Error:       errorText(result.err),
		StartedAt:   result.startedAt,
		FinishedAt:  result.finishedAt,
	}); err != nil {
		log.ErrorContext(ctx, "cannot record source run", "source", result.store, "country", result.country, "error", err)
	}

	if !result.status.OK() {
		// Failed sources keep their last known data: nothing is written for them.
		return
	}

	var planner storage.Planner
	if c.opts.Notifier != nil {
		planner = c.planner
	}
	scan, err := c.opts.DB.ApplyScan(ctx, storage.ScanInput{
		RunID:       runID,
		Store:       result.store,
		Country:     result.country,
		Offers:      result.offers,
		ObservedAt:  result.finishedAt,
		MarkMissing: result.status == model.SourceSuccess,
		Planner:     planner,
	})
	if err != nil {
		log.ErrorContext(ctx, "cannot persist offers", "source", result.store, "country", result.country, "error", err)
		sum.Failures = append(sum.Failures, fmt.Sprintf("%s/%s persist: %v", result.store, result.country, err))
		return
	}
	if result.status == model.SourcePartial {
		log.WarnContext(ctx, "source returned partial data", "source", result.store, "country", result.country,
			"offers", scan.Seen, "error", result.err)
	}

	sum.OffersSeen += scan.Seen
	sum.OffersNew += scan.New
	sum.OffersChanged += scan.Updated + scan.Reappeared
	sum.OffersMissing += scan.Missing
	sum.InvalidOffers += scan.Invalid
	sum.NotificationsCreated += scan.NotificationsCreated
	for _, rejected := range scan.Rejected {
		log.WarnContext(ctx, "adapter returned an invalid offer", "source", result.store, "country", result.country, "error", rejected)
	}

	log.InfoContext(ctx, "source scanned",
		"source", result.store,
		"country", result.country,
		"status", result.status,
		"duration_ms", result.duration.Milliseconds(),
		"offers_found", scan.Seen,
		"offers_new", scan.New,
		"offers_changed", scan.Updated+scan.Reappeared,
		"offers_missing", scan.Missing,
	)
}

// planner turns offer changes into notification intents.
func (c *Collector) planner(offer model.Offer, previous *model.Offer, change storage.Change, at time.Time) (storage.NotificationDraft, bool) {
	if !offer.Type.Notifiable() || offer.IsExpiredAt(at) {
		return storage.NotificationDraft{}, false
	}
	if offer.Availability != model.AvailabilityAvailable {
		return storage.NotificationDraft{}, false
	}

	var kind, dedupeKey string
	switch change {
	case storage.ChangeNew:
		kind = notify.KindNew
		dedupeKey = notify.KindNew + ":" + offer.Key()
	case storage.ChangeReappeared:
		kind = notify.KindReappeared
		dedupeKey = notify.KindReappeared + ":" + offer.Key() + ":" + strconv.FormatInt(at.Unix(), 10)
	case storage.ChangeUpdated:
		// Only a transition from upcoming to available is newsworthy; other
		// updates (description, price formatting) must not re-notify.
		if previous == nil || previous.Availability != model.AvailabilityUpcoming {
			return storage.NotificationDraft{}, false
		}
		kind = notify.KindNowAvailable
		dedupeKey = notify.KindNowAvailable + ":" + offer.Key() + ":" + deadlineKey(offer)
	default:
		return storage.NotificationDraft{}, false
	}

	return storage.NotificationDraft{
		Kind:      kind,
		DedupeKey: dedupeKey,
		Payload:   notify.RenderOffer(offer, kind),
	}, true
}

func deadlineKey(offer model.Offer) string {
	if offer.EndsAt.IsZero() {
		return "0"
	}
	return strconv.FormatInt(offer.EndsAt.Unix(), 10)
}

// fetchResult is the outcome of one adapter/country fetch.
type fetchResult struct {
	store      model.Store
	country    string
	offers     []model.Offer
	status     model.SourceStatus
	err        error
	duration   time.Duration
	startedAt  time.Time
	finishedAt time.Time
}

// fetchAll runs every adapter/country pair with bounded concurrency.
func (c *Collector) fetchAll(ctx context.Context) []fetchResult {
	type job struct {
		adapter stores.Adapter
		query   stores.Query
	}

	var jobs []job
	for _, adapter := range c.opts.Registry.All() {
		for _, country := range c.opts.Countries {
			jobs = append(jobs, job{
				adapter: adapter,
				query:   stores.Query{Country: country, Locale: c.opts.Locale},
			})
		}
	}

	results := make([]fetchResult, len(jobs))
	sem := make(chan struct{}, c.opts.MaxConcurrency)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(index int, j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[index] = c.fetchOne(ctx, j.adapter, j.query)
		}(i, j)
	}
	wg.Wait()
	return results
}

// fetchOne fetches a single adapter/country pair with its own timeout so one
// slow store cannot block the cycle.
func (c *Collector) fetchOne(ctx context.Context, adapter stores.Adapter, q stores.Query) fetchResult {
	result := fetchResult{store: adapter.Store(), country: q.Country, startedAt: c.opts.Now().UTC()}

	fetchCtx, cancel := context.WithTimeout(ctx, c.opts.SourceTimeout)
	defer cancel()

	offers, err := adapter.Fetch(fetchCtx, q)

	result.offers = offers
	result.err = err
	result.finishedAt = c.opts.Now().UTC()
	result.duration = result.finishedAt.Sub(result.startedAt)

	switch {
	case err == nil:
		result.status = model.SourceSuccess
	case len(offers) > 0:
		// The adapter produced usable data before failing: partial success.
		result.status = model.SourcePartial
	default:
		result.status = model.SourceFailure
	}
	return result
}

// newRunID builds a sortable, unique run identifier.
func newRunID(at time.Time) string {
	base := at.UTC().Format("20060102T150405Z")
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return base
	}
	return base + "-" + hex.EncodeToString(buf[:])
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
