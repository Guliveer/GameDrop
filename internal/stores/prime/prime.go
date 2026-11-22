// Package prime provides the optional Amazon Prime Gaming adapter.
//
// Prime Gaming loot is only reachable through Amazon endpoints that require an
// authenticated browser session (cookies), as the public GraphQL endpoint
// rejects anonymous requests and its queries are undocumented and change
// without notice. GameDrop deliberately does not ship cookie handling or a
// headless browser: storing Amazon session cookies in a scheduled worker is a
// security liability and the integration would break silently.
//
// The adapter therefore exists, is disabled by default (ENABLE_PRIME=false),
// and reports a clear, non-fatal error when it is enabled. Other adapters are
// unaffected because source failures are isolated.
package prime

import (
	"context"
	"errors"

	"github.com/Guliveer/GameDrop/internal/model"
	"github.com/Guliveer/GameDrop/internal/stores"
)

// ErrNotSupported explains the documented limitation of this adapter.
var ErrNotSupported = errors.New(
	"prime: Prime Gaming requires an authenticated Amazon session; " +
		"this adapter is intentionally disabled in v1 (see README, Prime Gaming)",
)

// Adapter is the disabled Prime Gaming adapter.
type Adapter struct{}

// New returns the Prime Gaming adapter.
func New() *Adapter { return &Adapter{} }

// Store returns the storefront identifier.
func (a *Adapter) Store() model.Store { return model.StorePrime }

// Fetch always returns ErrNotSupported: no unauthenticated, stable, and safe
// data source exists for Prime Gaming loot.
func (a *Adapter) Fetch(ctx context.Context, q stores.Query) ([]model.Offer, error) {
	return nil, ErrNotSupported
}
