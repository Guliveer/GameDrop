// Package model defines GameDrop's normalized domain model. Store adapters
// translate store-specific payloads into these types, and persistence and
// notifications never deal with store-specific structures.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Store identifies a storefront.
type Store string

// Supported storefronts.
const (
	StoreSteam Store = "steam"
	StoreEpic  Store = "epic"
	StoreGOG   Store = "gog"
	StorePrime Store = "prime"
)

var storeNames = map[Store]string{
	StoreSteam: "Steam",
	StoreEpic:  "Epic Games Store",
	StoreGOG:   "GOG",
	StorePrime: "Prime Gaming",
}

// ParseStore lowercases and trims a store name. Validity is checked by Valid.
func ParseStore(raw string) Store {
	return Store(strings.ToLower(strings.TrimSpace(raw)))
}

// Valid reports whether the store is one GameDrop knows about.
func (s Store) Valid() bool {
	_, ok := storeNames[s]
	return ok
}

// DisplayName returns a human readable store name.
func (s Store) DisplayName() string {
	if name, ok := storeNames[s]; ok {
		return name
	}
	return string(s)
}

// OfferType classifies a promotion. Adapters must only set FreeToKeep when the
// store payload provides explicit evidence (for example a verified 100%
// discount against a non-zero original price).
type OfferType string

// Offer classifications.
const (
	TypeFreeToKeep    OfferType = "free_to_keep"
	TypeFreeWeekend   OfferType = "free_weekend"
	TypePermanentFree OfferType = "permanent_free"
	TypeUnknown       OfferType = "unknown"
)

// ParseOfferType normalizes an offer type string. Unrecognized values become
// TypeUnknown so an adapter can never corrupt persisted data with a typo.
func ParseOfferType(raw string) OfferType {
	switch OfferType(strings.ToLower(strings.TrimSpace(raw))) {
	case TypeFreeToKeep:
		return TypeFreeToKeep
	case TypeFreeWeekend:
		return TypeFreeWeekend
	case TypePermanentFree:
		return TypePermanentFree
	default:
		return TypeUnknown
	}
}

// Valid reports whether the offer type is a known classification.
func (t OfferType) Valid() bool {
	switch t {
	case TypeFreeToKeep, TypeFreeWeekend, TypePermanentFree, TypeUnknown:
		return true
	default:
		return false
	}
}

// Notifiable reports whether a newly discovered offer of this type is worth a
// notification. Permanently free catalog entries are not.
func (t OfferType) Notifiable() bool {
	return t == TypeFreeToKeep || t == TypeFreeWeekend
}

// Label returns the English display label used in notifications.
func (t OfferType) Label() string {
	switch t {
	case TypeFreeToKeep:
		return "Free to keep"
	case TypeFreeWeekend:
		return "Free weekend"
	case TypePermanentFree:
		return "Permanently free"
	default:
		return "Unknown promotion"
	}
}

// Availability describes what we currently believe about an offer.
type Availability string

// Availability states.
const (
	AvailabilityAvailable Availability = "available"
	AvailabilityUpcoming  Availability = "upcoming"
	AvailabilityExpired   Availability = "expired"
	AvailabilityUnknown   Availability = "unknown"
)

// ParseAvailability normalizes an availability string, defaulting to unknown.
func ParseAvailability(raw string) Availability {
	switch Availability(strings.ToLower(strings.TrimSpace(raw))) {
	case AvailabilityAvailable:
		return AvailabilityAvailable
	case AvailabilityUpcoming:
		return AvailabilityUpcoming
	case AvailabilityExpired:
		return AvailabilityExpired
	default:
		return AvailabilityUnknown
	}
}

// Valid reports whether the availability is a known state.
func (a Availability) Valid() bool {
	switch a {
	case AvailabilityAvailable, AvailabilityUpcoming, AvailabilityExpired, AvailabilityUnknown:
		return true
	default:
		return false
	}
}

// Money holds an amount in minor units (cents) of a currency.
type Money struct {
	AmountMinor int64
	Currency    string
}

// IsZero reports whether the amount is exactly zero.
func (m Money) IsZero() bool { return m.AmountMinor == 0 }

// String renders the amount for display purposes only (not for calculations).
func (m Money) String() string {
	sign := ""
	amount := m.AmountMinor
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	unit := amount / 100
	frac := amount % 100
	return fmt.Sprintf("%s%d.%02d %s", sign, unit, frac, m.Currency)
}

// Offer is the canonical, store-agnostic representation of a free-game offer.
//
// Identity is (Store, Country, OfferID) and never the game title: the same game
// on two storefronts yields two offers, and a regional variant of the same
// store offer yields one offer per country.
type Offer struct {
	// Store is the storefront the offer was found on.
	Store Store

	// OfferID is the store's canonical identifier (Steam app ID, Epic offer ID,
	// GOG product ID, ...).
	OfferID string

	// Country is the ISO 3166-1 alpha-2 storefront country this offer was
	// observed for, or "" when the store does not expose country-specific data.
	Country string

	Title       string
	Description string
	URL         string
	ImageURL    string

	Type         OfferType
	Availability Availability

	// Price is the current price; nil when the store does not report one.
	Price *Money
	// OriginalPrice is the pre-discount price; nil when unknown.
	OriginalPrice *Money

	// StartsAt and EndsAt are the promotion window. The zero value means
	// unknown: adapters must never invent deadlines.
	StartsAt time.Time
	EndsAt   time.Time

	// Source records which store endpoint produced the offer, for example
	// "steam:appdetails". Useful when debugging conflicting classifications.
	Source string
}

// Key returns the canonical identity of the offer.
func (o Offer) Key() string {
	return string(o.Store) + ":" + o.Country + ":" + o.OfferID
}

// Normalize trims and upper/lowercases fields, applies safe defaults, and drops
// self-contradictory time windows. It is idempotent.
func (o *Offer) Normalize() {
	o.Store = ParseStore(string(o.Store))
	o.OfferID = strings.TrimSpace(o.OfferID)
	o.Country = strings.ToUpper(strings.TrimSpace(o.Country))
	o.Title = strings.TrimSpace(strings.ReplaceAll(o.Title, "\n", " "))
	o.Description = strings.TrimSpace(o.Description)
	o.URL = strings.TrimSpace(o.URL)
	o.ImageURL = strings.TrimSpace(o.ImageURL)
	o.Source = strings.TrimSpace(o.Source)
	o.Type = ParseOfferType(string(o.Type))
	o.Availability = ParseAvailability(string(o.Availability))

	if o.Price != nil {
		o.Price.Currency = strings.ToUpper(strings.TrimSpace(o.Price.Currency))
	}
	if o.OriginalPrice != nil {
		o.OriginalPrice.Currency = strings.ToUpper(strings.TrimSpace(o.OriginalPrice.Currency))
	}

	o.StartsAt = o.StartsAt.UTC().Truncate(time.Second)
	if o.StartsAt.IsZero() {
		o.StartsAt = time.Time{}
	}
	o.EndsAt = o.EndsAt.UTC().Truncate(time.Second)
	if o.EndsAt.IsZero() {
		o.EndsAt = time.Time{}
	}
	// A window that ends before it starts is contradictory evidence; keep the
	// deadline (the stronger signal) and drop the start.
	if !o.StartsAt.IsZero() && !o.EndsAt.IsZero() && o.EndsAt.Before(o.StartsAt) {
		o.StartsAt = time.Time{}
	}
}

// Validate checks the fields GameDrop relies on. Adapters returning invalid
// offers are logged and their offer is skipped, never persisted.
func (o Offer) Validate() error {
	if !o.Store.Valid() {
		return fmt.Errorf("offer: invalid store %q", o.Store)
	}
	if strings.TrimSpace(o.OfferID) == "" {
		return fmt.Errorf("offer: %s offer has an empty store offer ID", o.Store)
	}
	if strings.TrimSpace(o.Title) == "" {
		return fmt.Errorf("offer: %s offer %s has an empty title", o.Store, o.OfferID)
	}
	if !o.Type.Valid() {
		return fmt.Errorf("offer: %s offer %s has invalid type %q", o.Store, o.OfferID, o.Type)
	}
	if !o.Availability.Valid() {
		return fmt.Errorf("offer: %s offer %s has invalid availability %q", o.Store, o.OfferID, o.Availability)
	}
	for name, money := range map[string]*Money{"price": o.Price, "original price": o.OriginalPrice} {
		if money == nil {
			continue
		}
		if money.AmountMinor < 0 {
			return fmt.Errorf("offer: %s offer %s has negative %s", o.Store, o.OfferID, name)
		}
		if len(money.Currency) != 3 {
			return fmt.Errorf("offer: %s offer %s has invalid %s currency %q", o.Store, o.OfferID, name, money.Currency)
		}
	}
	return nil
}

// Fingerprint is a stable hash of the fields that describe an offer's state. It
// distinguishes a changed offer from a repeated observation of the same offer.
// Availability is included because an upcoming offer becoming available is a
// real change; bookkeeping timestamps are excluded because merely observing an
// offer again is not a change.
func (o Offer) Fingerprint() string {
	var b strings.Builder
	field := func(values ...string) {
		for _, v := range values {
			b.WriteString(v)
			b.WriteByte(0x1f)
		}
		b.WriteByte(0x1e)
	}
	field(string(o.Store), o.OfferID, o.Country, o.Title, o.Description, o.URL, o.ImageURL)
	field(string(o.Type), string(o.Availability), o.Source)
	field(moneyFingerprint(o.Price), moneyFingerprint(o.OriginalPrice))
	field(timestamp(o.StartsAt), timestamp(o.EndsAt))

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func moneyFingerprint(m *Money) string {
	if m == nil {
		return "-"
	}
	return strconv.FormatInt(m.AmountMinor, 10) + ":" + m.Currency
}

func timestamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return strconv.FormatInt(t.Unix(), 10)
}

// IsExpiredAt reports whether a confirmed deadline has passed. Offers without a
// known deadline never expire on their own.
func (o Offer) IsExpiredAt(now time.Time) bool {
	return !o.EndsAt.IsZero() && !o.EndsAt.After(now)
}

// IsNotifiableAt reports whether the offer is currently worth notifying about.
func (o Offer) IsNotifiableAt(now time.Time) bool {
	return o.Type.Notifiable() && !o.IsExpiredAt(now)
}
