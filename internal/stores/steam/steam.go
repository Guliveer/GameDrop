// Package steam discovers free promotions on the Steam storefront.
//
// Discovery uses the public "featuredcategories" endpoint, which lists items
// currently on sale. Only zero-priced candidates are considered, and each
// candidate is then verified against the public "appdetails" endpoint before it
// may be classified as FreeToKeep.
//
// Classification rules (deliberately conservative):
//
//   - appdetails reports is_free=true                -> PermanentFree
//   - price_overview: initial>0, final=0, 100% off   -> FreeToKeep (verified)
//   - price_overview: final>0 while the featured      -> FreeWeekend (heuristic:
//     listing advertised zero                          Steam free weekends keep
//     the purchase price)
//   - missing or contradictory evidence              -> Unknown
//
// Known limitation: Steam publishes no start date for these promotions, so
// upcoming free-to-keep offers cannot be reported. A verified promotion's end
// date is taken from the featured listing's discount_expiration.
package steam

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Guliveer/GameDrop/internal/httpclient"
	"github.com/Guliveer/GameDrop/internal/model"
	"github.com/Guliveer/GameDrop/internal/stores"
)

const (
	defaultFeaturedURL   = "https://store.steampowered.com/api/featuredcategories"
	defaultAppDetailsURL = "https://store.steampowered.com/api/appdetails"
	appPageURL           = "https://store.steampowered.com/app/%d/"

	// maxCandidates bounds verification work per fetch; Steam rarely has more
	// than a handful of fully discounted items at once.
	maxCandidates = 40
	// appDetailsBatch is how many app IDs are verified per request (Steam's
	// appdetails endpoint accepts a comma separated list).
	appDetailsBatch = 8
)

// Adapter implements stores.Adapter for Steam.
type Adapter struct {
	client *httpclient.Client

	// FeaturedURL and AppDetailsURL are override points for tests.
	FeaturedURL   string
	AppDetailsURL string

	// Now is the clock, overridable in tests.
	Now func() time.Time
}

// New returns a Steam adapter using the shared HTTP client.
func New(client *httpclient.Client) *Adapter {
	return &Adapter{
		client:        client,
		FeaturedURL:   defaultFeaturedURL,
		AppDetailsURL: defaultAppDetailsURL,
		Now:           time.Now,
	}
}

// Store returns the storefront identifier.
func (a *Adapter) Store() model.Store { return model.StoreSteam }

// Fetch discovers and verifies free Steam promotions for one country.
func (a *Adapter) Fetch(ctx context.Context, q stores.Query) ([]model.Offer, error) {
	country := strings.ToUpper(strings.TrimSpace(q.Country))
	if country == "" {
		return nil, fmt.Errorf("steam: country is required")
	}
	language := language(q.Locale)

	featured, err := a.fetchFeatured(ctx, country, language)
	if err != nil {
		return nil, err
	}

	candidates := featured.candidates()
	if len(candidates) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(candidates))
	for _, item := range candidates {
		ids = append(ids, item.ID)
	}
	details, detailsErr := a.fetchDetails(ctx, ids, country, language)

	offers := make([]model.Offer, 0, len(candidates))
	for _, item := range candidates {
		offer, ok := classify(item, details[item.ID], country)
		if ok {
			offers = append(offers, offer)
		}
	}
	if detailsErr != nil {
		// Return the offers we could classify, with the verification failure so
		// the collector records a partial success instead of losing data.
		return offers, fmt.Errorf("steam: verify %d candidate(s): %w", len(candidates), detailsErr)
	}
	return offers, nil
}

func (a *Adapter) fetchFeatured(ctx context.Context, country, language string) (featuredCategories, error) {
	var payload featuredCategories
	rawURL := fmt.Sprintf("%s?cc=%s&l=%s", a.FeaturedURL, url.QueryEscape(country), url.QueryEscape(language))
	if err := a.client.GetJSON(ctx, rawURL, &payload); err != nil {
		return payload, fmt.Errorf("steam: fetch featured categories: %w", err)
	}
	return payload, nil
}

// fetchDetails asks appdetails for each candidate batch. Failures are
// accumulated so a single unverifiable app cannot discard the whole scan.
func (a *Adapter) fetchDetails(ctx context.Context, ids []int64, country, language string) (map[int64]*appDetails, error) {
	details := make(map[int64]*appDetails, len(ids))
	var failed int
	var lastErr error

	for start := 0; start < len(ids); start += appDetailsBatch {
		end := min(start+appDetailsBatch, len(ids))
		batch := ids[start:end]

		parts := make([]string, 0, len(batch))
		for _, id := range batch {
			parts = append(parts, strconv.FormatInt(id, 10))
		}
		rawURL := fmt.Sprintf("%s?appids=%s&cc=%s&l=%s&filters=basic,price_overview",
			a.AppDetailsURL, strings.Join(parts, ","), url.QueryEscape(country), url.QueryEscape(language))

		var payload map[string]appDetailsEntry
		if err := a.client.GetJSON(ctx, rawURL, &payload); err != nil {
			failed += len(batch)
			lastErr = err
			continue
		}
		for _, id := range batch {
			entry, ok := payload[strconv.FormatInt(id, 10)]
			if !ok || !entry.Success {
				continue
			}
			details[id] = entry.Data
		}
	}

	if failed > 0 {
		return details, lastErr
	}
	return details, nil
}

// classify turns one featured candidate plus its verification into an offer.
// ok is false when the item is not a game promotion GameDrop should store.
func classify(item featuredItem, detail *appDetails, country string) (model.Offer, bool) {
	if item.ID == 0 {
		return model.Offer{}, false
	}

	title := strings.TrimSpace(item.Name)
	description := ""
	image := firstNonEmpty(item.OfferImage, item.HeaderImage, item.LargeCapsuleImage)

	if detail != nil {
		if detail.Type != "" && !strings.EqualFold(detail.Type, "game") {
			// DLC, demos, videos, and hardware are not game giveaways.
			return model.Offer{}, false
		}
		title = firstNonEmpty(strings.TrimSpace(detail.Name), title)
		description = strings.TrimSpace(detail.ShortDescription)
		image = firstNonEmpty(strings.TrimSpace(detail.HeaderImage), image)
	}
	if title == "" {
		title = fmt.Sprintf("Steam app %d", item.ID)
	}

	offer := model.Offer{
		Store:        model.StoreSteam,
		OfferID:      strconv.FormatInt(item.ID, 10),
		Country:      country,
		Title:        title,
		Description:  description,
		URL:          fmt.Sprintf(appPageURL, item.ID),
		ImageURL:     image,
		Type:         model.TypeUnknown,
		Availability: model.AvailabilityAvailable,
		Source:       "steam:featuredcategories+appdetails",
	}
	if item.DiscountExpiration > 0 {
		offer.EndsAt = time.Unix(item.DiscountExpiration, 0).UTC()
	}

	currency := item.Currency
	if detail != nil && detail.PriceOverview != nil {
		currency = firstNonEmpty(detail.PriceOverview.Currency, currency)
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))

	if detail == nil {
		// No verification evidence: keep whatever the listing told us, but do
		// not guess a classification.
		setPrices(&offer, item, currency)
		offer.Source = "steam:featuredcategories"
		return offer, true
	}

	switch {
	case detail.IsFree:
		offer.Type = model.TypePermanentFree
		setPrices(&offer, item, currency)
	case detail.PriceOverview != nil:
		po := detail.PriceOverview
		setVerifiedPrices(&offer, po)
		if po.Initial > 0 && po.Final == 0 && po.DiscountPercent == 100 {
			offer.Type = model.TypeFreeToKeep
		} else if po.Final > 0 {
			// Listed as free but still sold at a price: this is how Steam free
			// weekends surface through the public JSON endpoints.
			offer.Type = model.TypeFreeWeekend
		}
	default:
		setPrices(&offer, item, currency)
	}

	if offer.Type == model.TypeFreeToKeep || offer.Type == model.TypeFreeWeekend {
		if offer.Price == nil {
			offer.Price = &model.Money{Currency: currency}
		}
		if offer.Type == model.TypeFreeToKeep && offer.OriginalPrice == nil && item.OriginalPrice > 0 && len(currency) == 3 {
			offer.OriginalPrice = &model.Money{AmountMinor: item.OriginalPrice, Currency: currency}
		}
	}
	return offer, true
}

// setPrices copies the featured listing prices when they are usable.
func setPrices(offer *model.Offer, item featuredItem, currency string) {
	if len(currency) != 3 {
		return
	}
	offer.Price = &model.Money{AmountMinor: item.FinalPrice, Currency: currency}
	if item.OriginalPrice > 0 {
		offer.OriginalPrice = &model.Money{AmountMinor: item.OriginalPrice, Currency: currency}
	}
}

// setVerifiedPrices copies verified appdetails prices.
func setVerifiedPrices(offer *model.Offer, po *priceOverview) {
	currency := strings.ToUpper(strings.TrimSpace(po.Currency))
	if len(currency) != 3 {
		return
	}
	offer.Price = &model.Money{AmountMinor: po.Final, Currency: currency}
	if po.Initial > 0 {
		offer.OriginalPrice = &model.Money{AmountMinor: po.Initial, Currency: currency}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// language maps a locale to the language code Steam's API expects.
func language(locale string) string {
	switch stores.LocaleLanguage(locale) {
	case "pl":
		return "polish"
	case "de":
		return "german"
	case "fr":
		return "french"
	case "es":
		return "spanish"
	case "it":
		return "italian"
	case "pt":
		return "portuguese"
	case "ru":
		return "russian"
	case "tr":
		return "turkish"
	case "nl":
		return "dutch"
	case "cs":
		return "czech"
	case "uk":
		return "ukrainian"
	default:
		return "english"
	}
}

// --- wire types -------------------------------------------------------------

type featuredCategories struct {
	Specials    featuredGroup `json:"specials"`
	NewReleases featuredGroup `json:"new_releases"`
	TopSellers  featuredGroup `json:"top_sellers"`
	ComingSoon  featuredGroup `json:"coming_soon"`
}

type featuredGroup struct {
	Name  string         `json:"name"`
	Items []featuredItem `json:"items"`
}

type featuredItem struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Discounted         bool   `json:"discounted"`
	DiscountPercent    int    `json:"discount_percent"`
	OriginalPrice      int64  `json:"original_price"`
	FinalPrice         int64  `json:"final_price"`
	Currency           string `json:"currency"`
	DiscountExpiration int64  `json:"discount_expiration"`
	LargeCapsuleImage  string `json:"large_capsule_image"`
	HeaderImage        string `json:"header_image"`
	OfferImage         string `json:"offer_image"`
}

// candidates returns the fully discounted zero-price entries from the listing,
// de-duplicated by app ID and capped at maxCandidates.
func (f featuredCategories) candidates() []featuredItem {
	seen := make(map[int64]bool)
	var out []featuredItem
	for _, group := range []featuredGroup{f.Specials, f.NewReleases, f.TopSellers, f.ComingSoon} {
		for _, item := range group.Items {
			if item.ID == 0 || seen[item.ID] {
				continue
			}
			if item.FinalPrice != 0 || item.DiscountPercent != 100 {
				continue
			}
			seen[item.ID] = true
			out = append(out, item)
			if len(out) >= maxCandidates {
				return out
			}
		}
	}
	return out
}

type appDetailsEntry struct {
	Success bool        `json:"success"`
	Data    *appDetails `json:"data"`
}

type appDetails struct {
	Type             string         `json:"type"`
	Name             string         `json:"name"`
	SteamAppID       int64          `json:"steam_appid"`
	IsFree           bool           `json:"is_free"`
	ShortDescription string         `json:"short_description"`
	HeaderImage      string         `json:"header_image"`
	PriceOverview    *priceOverview `json:"price_overview"`
}

type priceOverview struct {
	Currency        string `json:"currency"`
	Initial         int64  `json:"initial"`
	Final           int64  `json:"final"`
	DiscountPercent int    `json:"discount_percent"`
}
