package gog

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

func newAdapter(t *testing.T, handler http.Handler) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	adapter := New(httpclient.New(httpclient.Options{
		Timeout:    5 * time.Second,
		MaxRetries: 0,
		BaseDelay:  time.Millisecond,
	}))
	adapter.CatalogURL = srv.URL + "/v1/catalog"
	return adapter
}

func fixturePages(t *testing.T) (*Adapter, *[]string) {
	t.Helper()
	page1, err := os.ReadFile("testdata/catalog_page1.json")
	if err != nil {
		t.Fatalf("read page 1 fixture: %v", err)
	}
	page2, err := os.ReadFile("testdata/catalog_page2.json")
	if err != nil {
		t.Fatalf("read page 2 fixture: %v", err)
	}

	var (
		mu      sync.Mutex
		queries []string
	)
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			w.Write(page2)
			return
		}
		w.Write(page1)
	}))
	adapter.MaxPages = 2
	return adapter, &queries
}

func TestFetchDetectsGiveawaysAcrossPages(t *testing.T) {
	adapter, queries := fixturePages(t)

	offers, err := adapter.Fetch(context.Background(), stores.Query{Country: "pl", Locale: "pl-PL"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("expected exactly one giveaway, got %d: %+v", len(offers), offers)
	}

	offer := offers[0]
	if offer.OfferID != "111222" {
		t.Errorf("offer ID = %q", offer.OfferID)
	}
	if offer.Type != model.TypeFreeToKeep {
		t.Errorf("type = %q, want free_to_keep", offer.Type)
	}
	if offer.Availability != model.AvailabilityAvailable {
		t.Errorf("availability = %q", offer.Availability)
	}
	if offer.Price == nil || offer.Price.AmountMinor != 0 || offer.Price.Currency != "PLN" {
		t.Errorf("price = %+v", offer.Price)
	}
	if offer.OriginalPrice == nil || offer.OriginalPrice.AmountMinor != 5999 {
		t.Errorf("original price = %+v", offer.OriginalPrice)
	}
	if !offer.EndsAt.IsZero() {
		t.Errorf("EndsAt = %s, want unknown: GOG exposes no reliable deadline", offer.EndsAt)
	}
	if offer.URL != "https://www.gog.com/pl/game/giveaway_game" {
		t.Errorf("URL = %q", offer.URL)
	}
	if offer.Country != "PL" {
		t.Errorf("country = %q", offer.Country)
	}

	// Pagination and the fixed query shape.
	if len(*queries) != 2 {
		t.Fatalf("requests = %d, want 2", len(*queries))
	}
	for i, want := range []string{"page=1", "page=2"} {
		for _, fragment := range []string{"countryCode=PL", "locale=pl-PL", "currencyCode=PLN", "discounted=true", want} {
			if !strings.Contains((*queries)[i], fragment) {
				t.Errorf("query %d (%q) missing %q", i, (*queries)[i], fragment)
			}
		}
	}
}

func TestFetchStopsOnEmptyPage(t *testing.T) {
	var hits int
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"products":[]}`))
	}))
	adapter.MaxPages = 3

	offers, err := adapter.Fetch(context.Background(), stores.Query{Country: "US", Locale: "en-US"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("expected no offers, got %d", len(offers))
	}
	if hits != 1 {
		t.Errorf("requests = %d, want 1: an empty page ends the scan", hits)
	}
}

func TestFetchPartialOnLaterPageFailure(t *testing.T) {
	page1, err := os.ReadFile("testdata/catalog_page1.json")
	if err != nil {
		t.Fatalf("read page 1 fixture: %v", err)
	}
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write(page1)
	}))
	adapter.MaxPages = 2

	// Page 1 has no giveaway, so a page 2 failure yields no offers plus an
	// error, which the collector records as a failed source.
	_, err = adapter.Fetch(context.Background(), stores.Query{Country: "PL", Locale: "pl-PL"})
	if err == nil {
		t.Fatal("expected an error when a page fails")
	}
}

func TestFetchMalformed(t *testing.T) {
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"products":`))
	}))
	if _, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL"}); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestFetchUnexpectedStructure(t *testing.T) {
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"products":{"not":"a list"}}`))
	}))
	if _, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL"}); err == nil {
		t.Fatal("expected an error for an unexpected structure")
	}
}

func TestFetchHTTPFailure(t *testing.T) {
	adapter := newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
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

func TestParseMoney(t *testing.T) {
	cases := []struct {
		in     money
		want   int64
		wantOK bool
	}{
		{money{Amount: "0.00", Currency: "PLN"}, 0, true},
		{money{Amount: "59.99", Currency: "PLN"}, 5999, true},
		{money{Amount: "59,99", Currency: "pln"}, 5999, true},
		{money{Amount: "5", Currency: "USD"}, 500, true},
		{money{Amount: "1.999", Currency: "USD"}, 199, true},
		{money{Amount: "", Currency: "USD"}, 0, false},
		{money{Amount: "10.00", Currency: "PL"}, 0, false},
		{money{Amount: "abc", Currency: "USD"}, 0, false},
	}
	for _, c := range cases {
		got, ok := parseMoney(c.in)
		if ok != c.wantOK || (ok && got.AmountMinor != c.want) {
			t.Errorf("parseMoney(%+v) = %+v, %v; want amount %d ok=%v", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestDiscountParsing(t *testing.T) {
	cases := []struct {
		raw  string
		full bool
	}{
		{`"-100%"`, true},
		{`"-50%"`, false},
		{`100`, true},
		{`50`, false},
		{`""`, false},
		{`null`, false},
	}
	for _, c := range cases {
		var d discount
		if err := d.UnmarshalJSON([]byte(c.raw)); err != nil {
			t.Fatalf("UnmarshalJSON(%s): %v", c.raw, err)
		}
		if got := d.isFull(); got != c.full {
			t.Errorf("discount(%s).isFull() = %v, want %v", c.raw, got, c.full)
		}
	}
}
