package steam

import (
	"context"
	"errors"
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

func newTestClient() *httpclient.Client {
	return httpclient.New(httpclient.Options{
		Timeout:    5 * time.Second,
		MaxRetries: 0,
		BaseDelay:  time.Millisecond,
		UserAgent:  "GameDropTest/1.0",
	})
}

// newServer serves the recorded fixtures and records request queries.
func newServer(t *testing.T) (*httptest.Server, *requestLog) {
	t.Helper()
	featured, err := os.ReadFile("testdata/featuredcategories.json")
	if err != nil {
		t.Fatalf("read featured fixture: %v", err)
	}
	details, err := os.ReadFile("testdata/appdetails.json")
	if err != nil {
		t.Fatalf("read appdetails fixture: %v", err)
	}

	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/featuredcategories":
			w.Write(featured)
		case "/api/appdetails":
			w.Write(details)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

type requestLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *requestLog) record(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, r.URL.Path+"?"+r.URL.RawQuery)
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func TestFetchClassifiesOffers(t *testing.T) {
	srv, log := newServer(t)
	adapter := New(newTestClient())
	adapter.FeaturedURL = srv.URL + "/api/featuredcategories"
	adapter.AppDetailsURL = srv.URL + "/api/appdetails"

	offers, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL", Locale: "pl-PL"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	byID := map[string]model.Offer{}
	for _, offer := range offers {
		byID[offer.OfferID] = offer
	}

	if len(byID) != 4 {
		t.Fatalf("expected 4 offers (free-to-keep, permanent free, unknown, free weekend), got %d: %+v", len(byID), byID)
	}

	verified, ok := byID["1001"]
	if !ok {
		t.Fatal("verified giveaway 1001 missing")
	}
	if verified.Type != model.TypeFreeToKeep {
		t.Errorf("1001 type = %q, want free_to_keep", verified.Type)
	}
	if verified.Country != "PL" {
		t.Errorf("1001 country = %q, want PL", verified.Country)
	}
	if verified.Price == nil || verified.Price.AmountMinor != 0 {
		t.Errorf("1001 price = %+v, want 0", verified.Price)
	}
	if verified.OriginalPrice == nil || verified.OriginalPrice.AmountMinor != 5999 {
		t.Errorf("1001 original price = %+v, want 5999", verified.OriginalPrice)
	}
	if verified.EndsAt.IsZero() {
		t.Error("1001 should keep the listing discount expiration as deadline")
	}
	if verified.URL != "https://store.steampowered.com/app/1001/" {
		t.Errorf("1001 URL = %q", verified.URL)
	}
	if !strings.Contains(verified.ImageURL, "1001-verified") {
		t.Errorf("1001 image = %q, want appdetails header image", verified.ImageURL)
	}

	permanent := byID["1002"]
	if permanent.Type != model.TypePermanentFree {
		t.Errorf("1002 type = %q, want permanent_free", permanent.Type)
	}

	unknown := byID["1003"]
	if unknown.Type != model.TypeUnknown {
		t.Errorf("1003 type = %q, want unknown when verification fails", unknown.Type)
	}

	if _, exists := byID["1005"]; exists {
		t.Error("discounted paid game 1005 must not be reported")
	}
	if _, exists := byID["1006"]; exists {
		t.Error("DLC 1006 must not be reported")
	}

	// Free weekend: advertised as free but appdetails still lists a price.
	// 1004 has a verified appdetails entry, so it must be reported as such.
	weekend, ok := byID["1004"]
	if !ok {
		t.Fatal("free weekend 1004 missing")
	}
	if weekend.Type != model.TypeFreeWeekend {
		t.Errorf("1004 type = %q, want free_weekend", weekend.Type)
	}

	calls := log.all()
	if len(calls) != 2 {
		t.Fatalf("expected one listing request and one verification request, got %v", calls)
	}
	for _, call := range calls {
		if !strings.Contains(call, "cc=PL") || !strings.Contains(call, "l=polish") {
			t.Errorf("request %q must carry country and mapped language", call)
		}
	}
	if !strings.Contains(calls[0], "/api/featuredcategories") {
		t.Errorf("first request = %q", calls[0])
	}
}

func TestFetchEmptyListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"specials":{"items":[]}}`))
	}))
	defer srv.Close()

	adapter := New(newTestClient())
	adapter.FeaturedURL = srv.URL
	adapter.AppDetailsURL = srv.URL

	offers, err := adapter.Fetch(context.Background(), stores.Query{Country: "US", Locale: "en-US"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("expected no offers, got %d", len(offers))
	}
}

func TestFetchMalformedFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"specials":`))
	}))
	defer srv.Close()

	adapter := New(newTestClient())
	adapter.FeaturedURL = srv.URL
	adapter.AppDetailsURL = srv.URL

	if _, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL", Locale: "pl-PL"}); err == nil {
		t.Fatal("expected a decode error for a malformed listing")
	}
}

func TestFetchUnexpectedStructure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"specials":{"items":"not-a-list"}}`))
	}))
	defer srv.Close()

	adapter := New(newTestClient())
	adapter.FeaturedURL = srv.URL
	adapter.AppDetailsURL = srv.URL

	if _, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL", Locale: "pl-PL"}); err == nil {
		t.Fatal("expected an error for an unexpected response structure")
	}
}

func TestFetchHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	adapter := New(newTestClient())
	adapter.FeaturedURL = srv.URL
	adapter.AppDetailsURL = srv.URL

	_, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL", Locale: "pl-PL"})
	if err == nil {
		t.Fatal("expected an HTTP failure")
	}
	var statusErr *httpclient.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected StatusError, got %T: %v", err, err)
	}
}

func TestFetchRequiresCountry(t *testing.T) {
	adapter := New(newTestClient())
	if _, err := adapter.Fetch(context.Background(), stores.Query{}); err == nil {
		t.Fatal("expected an error when no country is configured")
	}
}

func TestClassifyMissingNameFallsBackToAppID(t *testing.T) {
	offer, ok := classify(featuredItem{ID: 42, FinalPrice: 0, DiscountPercent: 100}, nil, "PL")
	if !ok {
		t.Fatal("expected a stored offer for an unverifiable candidate")
	}
	if offer.Title != "Steam app 42" {
		t.Errorf("title = %q", offer.Title)
	}
	if offer.Type != model.TypeUnknown {
		t.Errorf("type = %q, want unknown", offer.Type)
	}
}

func TestLanguageMapping(t *testing.T) {
	cases := map[string]string{
		"pl-PL": "polish",
		"en-US": "english",
		"de-DE": "german",
		"xx-YY": "english",
		"":      "english",
	}
	for locale, want := range cases {
		if got := language(locale); got != want {
			t.Errorf("language(%q) = %q, want %q", locale, got, want)
		}
	}
}
