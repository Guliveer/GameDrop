// Package notify renders offer notifications and delivers them through a
// Sender. Telegram is the only implementation in v1.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Guliveer/GameDrop/internal/model"
)

// Message is a rendered notification ready for delivery.
type Message struct {
	Text     string
	ImageURL string
}

// Sender delivers one message. Implementations return a *DeliveryError to
// describe transient failures.
type Sender interface {
	// Name identifies the delivery channel in logs.
	Name() string
	Send(ctx context.Context, msg Message) error
}

// DeliveryError describes a failed delivery attempt.
type DeliveryError struct {
	// Retryable reports whether a later attempt may succeed.
	Retryable bool
	// RetryAfter is a server requested delay, when provided.
	RetryAfter time.Duration
	// Err is the underlying cause.
	Err error
}

func (e *DeliveryError) Error() string {
	if e.Err == nil {
		return "notify: delivery failed"
	}
	return "notify: " + e.Err.Error()
}

// Unwrap exposes the underlying cause.
func (e *DeliveryError) Unwrap() error { return e.Err }

// IsRetryable reports whether a delivery error should be retried.
func IsRetryable(err error) bool {
	var deliveryErr *DeliveryError
	if errors.As(err, &deliveryErr) {
		return deliveryErr.Retryable
	}
	return false
}

// RetryAfter returns a server requested delay for a delivery error, if any.
func RetryAfter(err error) (time.Duration, bool) {
	var deliveryErr *DeliveryError
	if errors.As(err, &deliveryErr) && deliveryErr.RetryAfter > 0 {
		return deliveryErr.RetryAfter, true
	}
	return 0, false
}

// Notification kinds. They are stored in the outbox and shape the message.
const (
	KindNew          = "new"
	KindNowAvailable = "now_available"
	KindReappeared   = "reappeared"
	kindUnknownText  = "Free game"
)

// RenderOffer builds the plain-text message for an offer. Plain text (no
// Telegram parse mode) keeps titles with underscores or asterisks from breaking
// the message.
func RenderOffer(offer model.Offer, kind string) string {
	var b strings.Builder

	header := kindUnknownText
	switch kind {
	case KindNew:
		header = "Free game"
	case KindNowAvailable:
		header = "Now available"
	case KindReappeared:
		header = "Back again"
	}

	fmt.Fprintf(&b, "🎮 %s on %s\n\n", header, offer.Store.DisplayName())
	b.WriteString(offer.Title)
	b.WriteString("\n")

	if offer.Type != model.TypeUnknown {
		fmt.Fprintf(&b, "Offer: %s\n", offer.Type.Label())
	}
	if offer.Country != "" {
		fmt.Fprintf(&b, "Store region: %s\n", offer.Country)
	}
	if offer.OriginalPrice != nil && offer.OriginalPrice.AmountMinor > 0 {
		fmt.Fprintf(&b, "Price: free (was %s)\n", offer.OriginalPrice)
	} else {
		b.WriteString("Price: free\n")
	}

	if !offer.EndsAt.IsZero() {
		fmt.Fprintf(&b, "Claim by: %s\n", offer.EndsAt.Format("Mon, 02 Jan 2006 15:04 MST"))
	} else {
		b.WriteString("Claim by: unknown (check the store page)\n")
	}

	if offer.URL != "" {
		b.WriteString("\n")
		b.WriteString(offer.URL)
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}
