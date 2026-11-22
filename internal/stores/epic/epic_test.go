package epic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Guliveer/GameDrop/internal/httpclient"
	"github.com/Guliveer/GameDrop/internal/model"
	"github.com/Guliveer/GameDrop/internal/stores"
)

var testNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func newAdapter(t *testing.T, handler http.Handler) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	adapter := New(httpclient.New(httpclient.Options{
		Timeout:    5 * time.Second,
		MaxRetries: 0,
		BaseDelay:  time.Millisecond,
	}))
	adapter.FeedURL = srv.URL + "/freeGamesPromotions"
	adapter.Now = func() time.Time { return testNow }
	return adapter
}

func fixtureServer(t *testing.T) (*Adapter, *[]string) {
	t.Helper()
	body, err := os.ReadFile("testdata/free_games.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var (
		mu  sync.Mutex
		got []string
	)
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	return adapter, &got
}

func TestFetchCurrentAndUpcoming(t *testing.T) {
	adapter, queries := fixtureServer(t)

	offers, err := adapter.Fetch(context.Background(), stores.Query{Country: "pl", Locale: "pl-PL"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("expected 2 offers (current + upcoming), got %d: %+v", len(offers), offers)
	}

	byID := map[string]model.Offer{}
	for _, offer := range offers {
		byID[offer.OfferID] = offer
	}

	current, ok := byID["offer-current"]
	if !ok {
		t.Fatal("current giveaway missing")
	}
	if current.Type != model.TypeFreeToKeep {
		t.Errorf("current type = %q", current.Type)
	}
	if current.Availability != model.AvailabilityAvailable {
		t.Errorf("current availability = %q", current.Availability)
	}
	if current.Country != "PL" {
		t.Errorf("current country = %q", current.Country)
	}
	if current.OriginalPrice == nil || current.OriginalPrice.AmountMinor != 9999 {
		t.Errorf("current original price = %+v", current.OriginalPrice)
	}
	if current.Price == nil || current.Price.AmountMinor != 0 {
		t.Errorf("current price = %+v", current.Price)
	}
	if current.EndsAt.Format(time.RFC3339) != "2026-11-26T16:00:00Z" {
		t.Errorf("current end = %s", current.EndsAt)
	}
	if current.URL != "https://store.epicgames.com/pl/p/current-giveaway" {
		t.Errorf("current URL = %q", current.URL)
	}
	if !strings.Contains(current.ImageURL, "current-wide") {
		t.Errorf("current image = %q, want the wide key image", current.ImageURL)
	}

	upcoming, ok := byID["offer-upcoming"]
	if !ok {
		t.Fatal("upcoming giveaway missing")
	}
	if upcoming.Availability != model.AvailabilityUpcoming {
		t.Errorf("upcoming availability = %q", upcoming.Availability)
	}
	if upcoming.Type != model.TypeFreeToKeep {
		t.Errorf("upcoming type = %q", upcoming.Type)
	}
	// The product slug "upcoming-giveaway/home" must be normalized.
	if upcoming.URL != "https://store.epicgames.com/pl/p/upcoming-giveaway" {
		t.Errorf("upcoming URL = %q", upcoming.URL)
	}

	for _, id := range []string{"offer-free-forever", "offer-dlc", "offer-discount"} {
		if _, exists := byID[id]; exists {
			t.Errorf("%s must not be reported as a giveaway", id)
		}
	}

	if len(*queries) != 1 {
		t.Fatalf("expected one request, got %d", len(*queries))
	}
	q := (*queries)[0]
	for _, want := range []string{"country=PL", "allowCountries=PL", "locale=pl-PL"} {
		if !strings.Contains(q, want) {
			t.Errorf("request query %q missing %q", q, want)
		}
	}
}

func TestFetchEmptyFeed(t *testing.T) {
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"Catalog":{"searchStore":{"elements":[]}}}}`))
	}))

	offers, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL", Locale: "pl-PL"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("expected no offers, got %d", len(offers))
	}
}

func TestFetchMalformed(t *testing.T) {
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":`))
	}))
	if _, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL"}); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestFetchUnexpectedStructure(t *testing.T) {
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"Catalog":{"searchStore":{"elements":{"unexpected":true}}}}}`))
	}))
	if _, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL"}); err == nil {
		t.Fatal("expected an error for an unexpected structure")
	}
}

func TestFetchHTTPFailure(t *testing.T) {
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	if _, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL"}); err == nil {
		t.Fatal("expected an HTTP error")
	}
}

func TestFetchRequiresCountry(t *testing.T) {
	adapter := New(nil)
	if _, err := adapter.Fetch(context.Background(), stores.Query{}); err == nil {
		t.Fatal("expected an error without a country")
	}
}

func TestClassifyMissingFields(t *testing.T) {
	// No ID: cannot be identified and must be skipped.
	if _, ok := classify(element{Title: "No ID"}, "PL", "pl-PL", testNow); ok {
		t.Error("element without an ID must be skipped")
	}
	// No title: skipped, because notifications need a name.
	if _, ok := classify(element{ID: "x"}, "PL", "pl-PL", testNow); ok {
		t.Error("element without a title must be skipped")
	}
	// No promotion window: skipped.
	if _, ok := classify(element{
		ID: "x", Title: "X",
		Price: element{}.Price,
	}, "PL", "pl-PL", testNow); ok {
		t.Error("element without a promotion must be skipped")
	}
}

func TestPromotionWindowRespectsClock(t *testing.T) {
	// After the current window has ended, the same element must be skipped.
	adapter, _ := fixtureServer(t)
	adapter.Now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	offers, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL", Locale: "pl-PL"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("expected no offers after all windows ended, got %+v", offers)
	}
}
