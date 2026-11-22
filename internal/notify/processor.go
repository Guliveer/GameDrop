package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Guliveer/GameDrop/internal/storage"
)

// Processor drains the notification outbox.
//
// Each pending intent gets one delivery attempt per run. A transient failure is
// rescheduled with a bounded backoff until MaxAttempts is reached, so a Telegram
// outage delays notifications instead of losing them. Delivery is deliberately
// at-least-once: Telegram has no idempotency key, so duplicates remain possible
// if a request succeeds after GameDrop recorded a failure.
type Processor struct {
	DB          *storage.DB
	Sender      Sender
	Logger      *slog.Logger
	MaxAttempts int
	// Limit bounds how many notifications are attempted per run.
	Limit int
	// Now is the clock, overridable in tests.
	Now func() time.Time
}

// DeliveryReport summarizes an outbox drain.
type DeliveryReport struct {
	Attempted int
	Sent      int
	Failed    int
	Deferred  int
}

// Run delivers pending notifications.
func (p *Processor) Run(ctx context.Context) (DeliveryReport, error) {
	var report DeliveryReport
	if p.Sender == nil {
		return report, nil
	}
	now := p.now()
	maxAttempts := p.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}

	pending, err := p.DB.PendingNotifications(ctx, now, maxAttempts, limit)
	if err != nil {
		return report, err
	}

	for _, n := range pending {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Attempted++

		sendErr := p.Sender.Send(ctx, Message{Text: n.Payload})
		if sendErr == nil {
			if err := p.DB.MarkNotificationSent(ctx, n.ID, now); err != nil {
				return report, err
			}
			report.Sent++
			p.log().DebugContext(ctx, "notification delivered", "notification_id", n.ID, "offer_id", n.OfferID, "kind", n.Kind)
			continue
		}

		attempts := n.Attempts + 1
		retryable := IsRetryable(sendErr) && attempts < maxAttempts
		var retryAt *time.Time
		if retryable {
			delay := backoff(attempts)
			if serverDelay, ok := RetryAfter(sendErr); ok && serverDelay > delay {
				delay = serverDelay
			}
			next := now.Add(delay)
			retryAt = &next
			report.Deferred++
		} else {
			report.Failed++
		}
		if err := p.DB.MarkNotificationFailed(ctx, n.ID, attempts, sendErr.Error(), retryAt, now); err != nil {
			return report, err
		}
		p.log().WarnContext(ctx, "notification delivery failed",
			"notification_id", n.ID, "offer_id", n.OfferID, "attempts", attempts,
			"retryable", retryable, "error", sendErr.Error())
	}

	return report, nil
}

func (p *Processor) log() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

func (p *Processor) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// backoff returns the delay before the next delivery attempt.
func backoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 1 * time.Minute
	case attempt == 2:
		return 5 * time.Minute
	default:
		return 30 * time.Minute
	}
}

// DescribeReport renders a report for logs.
func DescribeReport(r DeliveryReport) string {
	return fmt.Sprintf("attempted=%d sent=%d failed=%d deferred=%d", r.Attempted, r.Sent, r.Failed, r.Deferred)
}
