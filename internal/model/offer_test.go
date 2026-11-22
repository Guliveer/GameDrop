package model

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func baseOffer() Offer {
	return Offer{
		Store:        StoreEpic,
		OfferID:      "offer-1",
		Country:      "pl",
		Title:        "  Some Game  ",
		Type:         TypeFreeToKeep,
		Availability: AvailabilityAvailable,
		Price:        &Money{AmountMinor: 0, Currency: "pln"},
		OriginalPrice: &Money{
			AmountMinor: 9999,
			Currency:    "pln",
		},
		EndsAt: now.Add(48 * time.Hour),
	}
}

func TestNormalize(t *testing.T) {
	offer := baseOffer()
	offer.Type = "FREE_TO_KEEP"
	offer.Availability = ""
	offer.Normalize()

	if offer.Country != "PL" {
		t.Errorf("Country = %q", offer.Country)
	}
	if offer.Title != "Some Game" {
		t.Errorf("Title = %q", offer.Title)
	}
	if offer.Type != TypeFreeToKeep {
		t.Errorf("Type = %q", offer.Type)
	}
	if offer.Availability != AvailabilityUnknown {
		t.Errorf("Availability = %q, want unknown default", offer.Availability)
	}
	if offer.Price.Currency != "PLN" {
		t.Errorf("Currency = %q", offer.Price.Currency)
	}
}

func TestNormalizeDropsContradictoryWindow(t *testing.T) {
	offer := baseOffer()
	offer.StartsAt = now.Add(2 * time.Hour)
	offer.EndsAt = now.Add(-2 * time.Hour)
	offer.Normalize()
	if !offer.StartsAt.IsZero() {
		t.Errorf("StartsAt = %s, want dropped", offer.StartsAt)
	}
	if offer.EndsAt.IsZero() {
		t.Error("EndsAt must be preserved")
	}
}

func TestValidate(t *testing.T) {
	valid := baseOffer()
	valid.Normalize()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid offer rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Offer)
	}{
		{"unknown store", func(o *Offer) { o.Store = "gog2" }},
		{"empty id", func(o *Offer) { o.OfferID = "" }},
		{"empty title", func(o *Offer) { o.Title = " " }},
		{"invalid type", func(o *Offer) { o.Type = "giveaway" }},
		{"negative price", func(o *Offer) { o.Price = &Money{AmountMinor: -1, Currency: "PLN"} }},
		{"missing currency", func(o *Offer) { o.Price = &Money{AmountMinor: 0, Currency: ""} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offer := baseOffer()
			tc.mutate(&offer)
			if err := offer.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestIdentityIsStoreAndCountryAndID(t *testing.T) {
	offer := baseOffer()
	offer.Normalize()
	base := offer.Key()
	if base != "epic:PL:offer-1" {
		t.Fatalf("Key = %q", base)
	}

	regional := offer
	regional.Country = "US"
	if regional.Key() == base {
		t.Error("regional variants must have distinct identities")
	}

	otherStore := offer
	otherStore.Store = StoreGOG
	if otherStore.Key() == base {
		t.Error("the same game on another store must be a separate offer")
	}
}

func TestFingerprint(t *testing.T) {
	offer := baseOffer()
	offer.Normalize()
	original := offer.Fingerprint()

	// Availability is part of the state: upcoming -> available is a change.
	changedAvailability := offer
	changedAvailability.Availability = AvailabilityExpired
	if changedAvailability.Fingerprint() == original {
		t.Error("an availability change must change the fingerprint")
	}

	changedTitle := offer
	changedTitle.Title = "Other Game"
	if changedTitle.Fingerprint() == original {
		t.Error("a title change must change the fingerprint")
	}

	changedDeadline := offer
	changedDeadline.EndsAt = offer.EndsAt.Add(time.Hour)
	if changedDeadline.Fingerprint() == original {
		t.Error("a deadline change must change the fingerprint")
	}

	// Repeated observation of identical data must be stable.
	if offer.Fingerprint() != baseOffer().Fingerprint() {
		// baseOffer is not normalized; normalize a copy before comparing.
		other := baseOffer()
		other.Normalize()
		if offer.Fingerprint() != other.Fingerprint() {
			t.Error("fingerprint must be deterministic")
		}
	}
}

func TestExpirationAndNotifiability(t *testing.T) {
	offer := baseOffer()
	offer.Normalize()

	if offer.IsExpiredAt(now) {
		t.Error("offer with a future deadline is not expired")
	}
	if !offer.IsNotifiableAt(now) {
		t.Error("active free-to-keep offer must be notifiable")
	}

	expired := offer
	expired.EndsAt = now.Add(-time.Minute)
	if !expired.IsExpiredAt(now) {
		t.Error("offer with a past deadline is expired")
	}
	if expired.IsNotifiableAt(now) {
		t.Error("expired offer must not be notifiable")
	}

	noDeadline := offer
	noDeadline.EndsAt = time.Time{}
	if noDeadline.IsExpiredAt(now) {
		t.Error("an offer without a deadline never expires by itself")
	}

	permanent := offer
	permanent.Type = TypePermanentFree
	if permanent.IsNotifiableAt(now) {
		t.Error("permanently free products must not notify")
	}

	unknown := offer
	unknown.Type = TypeUnknown
	if unknown.IsNotifiableAt(now) {
		t.Error("unknown classifications must not notify")
	}
}

func TestOfferTypeLabelsAndNotifiability(t *testing.T) {
	if !TypeFreeToKeep.Notifiable() || !TypeFreeWeekend.Notifiable() {
		t.Error("free-to-keep and free weekend are notifiable")
	}
	if TypePermanentFree.Notifiable() || TypeUnknown.Notifiable() {
		t.Error("permanent free and unknown must not notify")
	}
	for _, typ := range []OfferType{TypeFreeToKeep, TypeFreeWeekend, TypePermanentFree, TypeUnknown} {
		if !typ.Valid() {
			t.Errorf("%q should be valid", typ)
		}
		if typ.Label() == "" {
			t.Errorf("%q has no label", typ)
		}
	}
	if ParseOfferType("nonsense") != TypeUnknown {
		t.Error("unknown offer types must normalize to unknown")
	}
}

func TestMoneyString(t *testing.T) {
	cases := []struct {
		money Money
		want  string
	}{
		{Money{AmountMinor: 0, Currency: "PLN"}, "0.00 PLN"},
		{Money{AmountMinor: 5999, Currency: "PLN"}, "59.99 PLN"},
		{Money{AmountMinor: 5, Currency: "USD"}, "0.05 USD"},
		{Money{AmountMinor: -250, Currency: "EUR"}, "-2.50 EUR"},
	}
	for _, c := range cases {
		if got := c.money.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}

func TestStoreHelpers(t *testing.T) {
	if ParseStore(" Steam ") != StoreSteam {
		t.Error("ParseStore must trim and lowercase")
	}
	if Store("steam").DisplayName() != "Steam" {
		t.Error("DisplayName for steam")
	}
	if Store("unknown-store").DisplayName() != "unknown-store" {
		t.Error("unknown stores should fall back to their raw name")
	}
	if Store("nope").Valid() {
		t.Error("unknown stores are invalid")
	}
	if !strings.Contains(TypeFreeToKeep.Label(), "keep") {
		t.Error("free-to-keep label")
	}
}
