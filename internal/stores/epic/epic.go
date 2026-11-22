// Package epic discovers free promotions on the Epic Games Store using the
// anonymous free-games feed (the same catalog data the storefront itself uses).
//
// Classification rules:
//
//   - A currently running promotional offer with discountPercentage 0 (Epic
//     expresses "100% off" as a zero percentage) and a non-zero original price
//     is FreeToKeep and Available.
//   - The same promotion starting in the future is FreeToKeep and Upcoming.
//   - Products with a zero original price are permanently free and are not
//     reported: they are not giveaways.
//   - DLC, add-ons, soundtracks, currency, demos and similar products are
//     skipped.
//
// The feed is anonymous and requires no API key. Epic's Browse GraphQL store
// endpoint is not used: it relies on unsigned persisted queries that change
// without notice, and the free-games feed already returns price, promotion
// window, and product identifiers. This keeps a third-party/undocumented API
// out of the critical path.
package epic

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Guliveer/GameDrop/internal/httpclient"
	"github.com/Guliveer/GameDrop/internal/model"
	"github.com/Guliveer/GameDrop/internal/stores"
)

const (
	defaultFeedURL = "https://store-site-backend-static.ak.epicgames.com/freeGamesPromotions"
	storePageURL   = "https://store.epicgames.com/%s/p/%s"
	bundlePageURL  = "https://store.epicgames.com/%s/bundles/%s"
)

// excludedOfferTypes lists Epic offer types that are never game giveaways.
var excludedOfferTypes = map[string]bool{
	"DLC":              true,
	"ADD_ON":           true,
	"CONSUMABLE":       true,
	"SOUNDTRACK":       true,
	"VIRTUAL_CURRENCY": true,
	"WALLET":           true,
	"DEMO":             true,
	"BUNDLE":           true, // Epic bundles are rarely giveaways; keep noise low
}

// Adapter implements stores.Adapter for the Epic Games Store.
type Adapter struct {
	client *httpclient.Client
	// FeedURL is an override point for tests.
	FeedURL string
	// Now is the clock, overridable in tests.
	Now func() time.Time
}

// New returns an Epic adapter using the shared HTTP client.
func New(client *httpclient.Client) *Adapter {
	return &Adapter{
		client:  client,
		FeedURL: defaultFeedURL,
		Now:     time.Now,
	}
}

// Store returns the storefront identifier.
func (a *Adapter) Store() model.Store { return model.StoreEpic }

// Fetch discovers current and upcoming Epic giveaways for one country.
func (a *Adapter) Fetch(ctx context.Context, q stores.Query) ([]model.Offer, error) {
	country := strings.ToUpper(strings.TrimSpace(q.Country))
	if country == "" {
		return nil, fmt.Errorf("epic: country is required")
	}
	locale := strings.TrimSpace(q.Locale)
	if locale == "" {
		locale = strings.ToLower(country)
	}
	now := a.Now().UTC()

	rawURL := fmt.Sprintf("%s?locale=%s&country=%s&allowCountries=%s",
		a.FeedURL, url.QueryEscape(locale), url.QueryEscape(country), url.QueryEscape(country))

	var payload feed
	if err := a.client.GetJSON(ctx, rawURL, &payload); err != nil {
		return nil, fmt.Errorf("epic: fetch free games feed: %w", err)
	}
	if len(payload.Data.Catalog.SearchStore.Elements) == 0 {
		return nil, nil
	}

	offers := make([]model.Offer, 0, len(payload.Data.Catalog.SearchStore.Elements))
	seen := make(map[string]bool)
	for _, element := range payload.Data.Catalog.SearchStore.Elements {
		offer, ok := classify(element, country, locale, now)
		if !ok || seen[offer.OfferID] {
			continue
		}
		seen[offer.OfferID] = true
		offers = append(offers, offer)
	}
	return offers, nil
}

// classify converts one feed element into an offer.
func classify(element element, country, locale string, now time.Time) (model.Offer, bool) {
	if element.ID == "" || strings.TrimSpace(element.Title) == "" {
		return model.Offer{}, false
	}
	if excludedOfferTypes[strings.ToUpper(strings.TrimSpace(element.OfferType))] {
		return model.Offer{}, false
	}

	price := element.Price.TotalPrice
	if price.OriginalPrice == 0 {
		// Permanently free catalog product, not a giveaway.
		return model.Offer{}, false
	}

	promo, upcoming, ok := element.promotion(now)
	if !ok {
		return model.Offer{}, false
	}

	offer := model.Offer{
		Store:        model.StoreEpic,
		OfferID:      element.ID,
		Country:      country,
		Title:        strings.TrimSpace(element.Title),
		Description:  strings.TrimSpace(element.Description),
		URL:          element.pageURL(locale),
		ImageURL:     element.imageURL(),
		Type:         model.TypeFreeToKeep,
		Availability: model.AvailabilityAvailable,
		Price:        &model.Money{AmountMinor: 0, Currency: strings.ToUpper(price.CurrencyCode)},
		OriginalPrice: &model.Money{
			AmountMinor: price.OriginalPrice,
			Currency:    strings.ToUpper(price.CurrencyCode),
		},
		StartsAt: promo.Start,
		EndsAt:   promo.End,
		Source:   "epic:freeGamesPromotions",
	}
	if upcoming {
		offer.Availability = model.AvailabilityUpcoming
	}
	if len(price.CurrencyCode) != 3 {
		// A price without a usable currency is not worth storing as a price.
		offer.Price = nil
		offer.OriginalPrice = nil
	}
	return offer, true
}

// promotion returns the active or upcoming 100% discount window. isUpcoming is
// true only for a promotion that has not started yet; ok is false when the
// element has no such promotion.
func (e element) promotion(now time.Time) (window, bool, bool) {
	for _, group := range e.Promotions.PromotionalOffers {
		for _, p := range group.PromotionalOffers {
			if !p.isFree() {
				continue
			}
			start, end := p.interval()
			if start.IsZero() || end.IsZero() {
				continue
			}
			if !now.Before(start) && now.Before(end) {
				return window{Start: start, End: end}, false, true
			}
		}
	}

	var best window
	for _, group := range e.Promotions.UpcomingPromotionalOffers {
		for _, p := range group.PromotionalOffers {
			if !p.isFree() {
				continue
			}
			start, end := p.interval()
			if start.IsZero() || end.IsZero() || !start.After(now) {
				continue
			}
			if best.Start.IsZero() || start.Before(best.Start) {
				best = window{Start: start, End: end}
			}
		}
	}
	if best.Start.IsZero() {
		return window{}, false, false
	}
	return best, true, true
}

// pageURL builds the storefront URL, preferring catalog mappings over slugs.
func (e element) pageURL(locale string) string {
	language := stores.LocaleLanguage(locale)
	if language == "" {
		language = "en"
	}
	slug := ""
	for _, m := range e.CatalogNs.Mappings {
		if s := strings.TrimSpace(m.PageSlug); s != "" {
			slug = s
		}
	}
	if slug == "" {
		slug = strings.TrimSpace(e.ProductSlug)
	}
	if slug == "" {
		slug = strings.TrimSpace(e.URLSlug)
	}
	if slug == "" {
		return ""
	}
	// Epic product slugs occasionally carry a "/home" suffix.
	if idx := strings.Index(slug, "/"); idx > 0 {
		slug = slug[:idx]
	}
	if strings.EqualFold(e.OfferType, "BUNDLE") {
		return fmt.Sprintf(bundlePageURL, language, slug)
	}
	return fmt.Sprintf(storePageURL, language, slug)
}

// imageURL returns the widest usable key image.
func (e element) imageURL() string {
	preferred := []string{"OfferImageWide", "DieselStoreFrontWide", "Thumbnail", "OfferImageTall"}
	for _, want := range preferred {
		for _, img := range e.KeyImages {
			if strings.EqualFold(img.Type, want) && strings.TrimSpace(img.URL) != "" {
				return strings.TrimSpace(img.URL)
			}
		}
	}
	for _, img := range e.KeyImages {
		if strings.TrimSpace(img.URL) != "" {
			return strings.TrimSpace(img.URL)
		}
	}
	return ""
}

// --- wire types -------------------------------------------------------------

type feed struct {
	Data struct {
		Catalog struct {
			SearchStore struct {
				Elements []element `json:"elements"`
			} `json:"searchStore"`
		} `json:"Catalog"`
	} `json:"data"`
}

type element struct {
	Title       string `json:"title"`
	ID          string `json:"id"`
	Namespace   string `json:"namespace"`
	Description string `json:"description"`
	OfferType   string `json:"offerType"`
	ProductSlug string `json:"productSlug"`
	URLSlug     string `json:"urlSlug"`
	KeyImages   []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"keyImages"`
	CatalogNs struct {
		Mappings []struct {
			PageSlug string `json:"pageSlug"`
			PageType string `json:"pageType"`
		} `json:"mappings"`
	} `json:"catalogNs"`
	Price struct {
		TotalPrice struct {
			DiscountPrice int64  `json:"discountPrice"`
			OriginalPrice int64  `json:"originalPrice"`
			CurrencyCode  string `json:"currencyCode"`
		} `json:"totalPrice"`
	} `json:"price"`
	Promotions struct {
		PromotionalOffers []struct {
			PromotionalOffers []promo `json:"promotionalOffers"`
		} `json:"promotionalOffers"`
		UpcomingPromotionalOffers []struct {
			PromotionalOffers []promo `json:"promotionalOffers"`
		} `json:"upcomingPromotionalOffers"`
	} `json:"promotions"`
}

type promo struct {
	StartDate       string `json:"startDate"`
	EndDate         string `json:"endDate"`
	DiscountSetting struct {
		DiscountType       string `json:"discountType"`
		DiscountPercentage int    `json:"discountPercentage"`
	} `json:"discountSetting"`
}

// isFree reports whether the promotion is a full discount. Epic reports a 100%
// discount as discountPercentage 0.
func (p promo) isFree() bool {
	return p.DiscountSetting.DiscountPercentage == 0
}

func (p promo) interval() (start, end time.Time) {
	return parseTime(p.StartDate), parseTime(p.EndDate)
}

type window struct {
	Start time.Time
	End   time.Time
}

// parseTime parses an Epic ISO-8601 timestamp, returning the zero time when the
// value is missing or malformed.
func parseTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
