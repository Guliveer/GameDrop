// Package stores defines the contract every storefront adapter implements and
// a small registry of enabled adapters. Adapters are independent: one failing
// adapter never prevents the others from producing offers.
package stores

import (
	"context"
	"fmt"
	"strings"

	"github.com/Guliveer/GameDrop/internal/model"
)

// Query carries the storefront country and locale for one fetch. Country is an
// uppercase ISO 3166-1 alpha-2 code and is intentionally independent from the
// machine's Fly.io region.
type Query struct {
	Country string
	Locale  string
}

// Adapter is one storefront integration.
type Adapter interface {
	// Store returns the storefront this adapter discovers offers for.
	Store() model.Store
	// Fetch discovers offers for one country. Returning offers together with an
	// error is allowed and is recorded as a partial success.
	Fetch(ctx context.Context, q Query) ([]model.Offer, error)
}

// Registry holds the enabled adapters in a stable order.
type Registry struct {
	adapters []Adapter
}

// NewRegistry builds a registry from adapters, skipping nil entries.
func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{}
	for _, a := range adapters {
		if a != nil {
			r.adapters = append(r.adapters, a)
		}
	}
	return r
}

// All returns the registered adapters.
func (r *Registry) All() []Adapter {
	out := make([]Adapter, len(r.adapters))
	copy(out, r.adapters)
	return out
}

// Len returns the number of registered adapters.
func (r *Registry) Len() int { return len(r.adapters) }

// LocaleLanguage returns the primary language subtag of a locale such as
// "pl-PL" ("pl"), falling back to the lowercase locale itself.
func LocaleLanguage(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return ""
	}
	if idx := strings.IndexAny(locale, "-_"); idx > 0 {
		return strings.ToLower(locale[:idx])
	}
	return strings.ToLower(locale)
}

// WrapError adds adapter context without losing the underlying error.
func WrapError(store model.Store, action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %s: %w", store, action, err)
}
