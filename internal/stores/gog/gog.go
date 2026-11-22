// Package gog discovers GOG giveaways.
//
// The public catalog API is the source. Two facts about it drive this adapter:
//
//   - Price filters (price=free, priceRange) are ignored by the server, so
//     filtering happens client-side: a product qualifies only when its final
//     price is exactly zero while its base price is non-zero. Permanently free
//     products have a zero base price and are excluded, so a zero price alone is
//     never treated as a giveaway.
//   - Giveaways are 100%-off promotions, so they always appear in the
//     "discounted" catalog. The adapter scans a bounded number of trending
//     discounted pages, which is where GOG promotes giveaways.
//
// Known limitation: GOG exposes no reliable giveaway end date, so EndsAt stays
// unknown instead of being invented. A giveaway that never surfaces in the
// scanned trending pages can be missed; raise MaxPages to scan deeper.
package gog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Guliveer/GameDrop/internal/httpclient"
	"github.com/Guliveer/GameDrop/internal/model"
	"github.com/Guliveer/GameDrop/internal/stores"
)

const (
	defaultCatalogURL = "https://catalog.gog.com/v1/catalog"
	pageLimit         = 48
	defaultMaxPages   = 3
	productPageURL    = "https://www.gog.com%s"
	fallbackGameURL   = "https://www.gog.com/en/game/%s"
)

// currencyByCountry maps storefront countries to the currency the catalog
// should price in. Countries without an entry fall back to GOG's default
// (USD), which is still a valid price with a currency code.
var currencyByCountry = map[string]string{
	"PL": "PLN",
	"US": "USD",
	"CA": "CAD",
	"GB": "GBP",
	"AU": "AUD",
	"BR": "BRL",
	"JP": "JPY",
	"IN": "INR",
	"TR": "TRY",
	"UA": "UAH",
	"RU": "RUB",
	"DE": "EUR",
	"FR": "EUR",
	"ES": "EUR",
	"IT": "EUR",
	"NL": "EUR",
	"BE": "EUR",
	"AT": "EUR",
	"PT": "EUR",
	"IE": "EUR",
	"FI": "EUR",
	"GR": "EUR",
	"SK": "EUR",
	"SI": "EUR",
	"LT": "EUR",
	"LV": "EUR",
	"EE": "EUR",
	"HR": "EUR",
	"CY": "EUR",
	"MT": "EUR",
	"LU": "EUR",
}

// Adapter implements stores.Adapter for GOG.
type Adapter struct {
	client *httpclient.Client
	// CatalogURL is an override point for tests.
	CatalogURL string
	// MaxPages bounds how many catalog pages are scanned per fetch.
	MaxPages int
}

// New returns a GOG adapter using the shared HTTP client.
func New(client *httpclient.Client) *Adapter {
	return &Adapter{
		client:     client,
		CatalogURL: defaultCatalogURL,
		MaxPages:   defaultMaxPages,
	}
}

// Store returns the storefront identifier.
func (a *Adapter) Store() model.Store { return model.StoreGOG }

// Fetch scans the trending discounted catalog for giveaways.
func (a *Adapter) Fetch(ctx context.Context, q stores.Query) ([]model.Offer, error) {
	country := strings.ToUpper(strings.TrimSpace(q.Country))
	if country == "" {
		return nil, fmt.Errorf("gog: country is required")
	}
	locale := strings.TrimSpace(q.Locale)
	if locale == "" {
		locale = strings.ToLower(country)
	}

	maxPages := a.MaxPages
	if maxPages < 1 {
		maxPages = 1
	}

	var offers []model.Offer
	seen := make(map[string]bool)

	for page := 1; page <= maxPages; page++ {
		payload, err := a.fetchPage(ctx, country, locale, page)
		if err != nil {
			if page == 1 {
				return nil, err
			}
			// Later pages are best-effort: keep what was already discovered.
			return offers, err
		}
		if len(payload.Products) == 0 {
			break
		}
		for _, product := range payload.Products {
			offer, ok := classify(product, country, locale)
			if !ok || seen[offer.OfferID] {
				continue
			}
			seen[offer.OfferID] = true
			offers = append(offers, offer)
		}
	}

	return offers, nil
}

func (a *Adapter) fetchPage(ctx context.Context, country, locale string, page int) (catalog, error) {
	rawURL := fmt.Sprintf("%s?countryCode=%s&locale=%s&productType=in:game&discounted=true&limit=%d&page=%d&order=desc:trending",
		a.CatalogURL, url.QueryEscape(country), url.QueryEscape(locale), pageLimit, page)
	if currency, ok := currencyByCountry[country]; ok {
		rawURL += "&currencyCode=" + url.QueryEscape(currency)
	}

	var payload catalog
	if err := a.client.GetJSON(ctx, rawURL, &payload); err != nil {
		return payload, fmt.Errorf("gog: fetch catalog page %d: %w", page, err)
	}
	return payload, nil
}

// classify converts one catalog product into an offer. ok is false for anything
// that is not a giveaway.
func classify(product product, country, locale string) (model.Offer, bool) {
	if product.ID == "" || strings.TrimSpace(product.Title) == "" {
		return model.Offer{}, false
	}
	current, okCurrent := parseMoney(product.Price.FinalMoney)
	original, okOriginal := parseMoney(product.Price.BaseMoney)
	if !okCurrent || !okOriginal {
		return model.Offer{}, false
	}
	// Giveaway evidence: a full discount from a paid base price to exactly zero.
	if current.AmountMinor != 0 || original.AmountMinor <= 0 {
		return model.Offer{}, false
	}
	// Corroborate with the advertised discount when GOG provides one.
	if product.Price.Discount != "" && !product.Price.Discount.isFull() {
		return model.Offer{}, false
	}

	return model.Offer{
		Store:         model.StoreGOG,
		OfferID:       product.ID,
		Country:       country,
		Title:         strings.TrimSpace(product.Title),
		URL:           product.pageURL(locale),
		ImageURL:      firstNonEmpty(product.CoverHorizontal, product.CoverVertical),
		Type:          model.TypeFreeToKeep,
		Availability:  model.AvailabilityAvailable,
		Price:         &current,
		OriginalPrice: &original,
		Source:        "gog:catalog",
	}, true
}

// pageURL prefers GOG's own store link and falls back to a localized slug URL.
func (p product) pageURL(locale string) string {
	link := strings.TrimSpace(p.StoreLink)
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}
	if strings.HasPrefix(link, "/") {
		return fmt.Sprintf(productPageURL, link)
	}
	if strings.TrimSpace(p.Slug) != "" {
		return fmt.Sprintf(fallbackGameURL, strings.TrimSpace(p.Slug))
	}
	return ""
}

// parseMoney converts GOG's decimal string amounts ("59.99") to minor units.
func parseMoney(m money) (model.Money, bool) {
	currency := strings.ToUpper(strings.TrimSpace(m.Currency))
	if len(currency) != 3 {
		return model.Money{}, false
	}
	amount := strings.TrimSpace(m.Amount)
	if amount == "" {
		return model.Money{}, false
	}
	// Accept both "59.99" and comma decimal separators.
	amount = strings.ReplaceAll(amount, ",", ".")
	whole, frac, _ := strings.Cut(amount, ".")
	wholeValue, err := strconv.ParseInt(strings.TrimSpace(whole), 10, 64)
	if err != nil {
		return model.Money{}, false
	}
	var fracValue int64
	if frac != "" {
		if len(frac) > 2 {
			frac = frac[:2]
		}
		for len(frac) < 2 {
			frac += "0"
		}
		fracValue, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return model.Money{}, false
		}
	}
	return model.Money{AmountMinor: wholeValue*100 + fracValue, Currency: currency}, true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// discount is GOG's advertised discount, which is a string ("-100%") in the
// catalog API and a number in some other responses. Empty means "not provided".
type discount string

// UnmarshalJSON accepts a string, a number, or null.
func (d *discount) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" || trimmed == `""` {
		*d = ""
		return nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		*d = discount(asString)
		return nil
	}
	var asNumber float64
	if err := json.Unmarshal(raw, &asNumber); err != nil {
		return nil // unknown shape: treat as "not provided"
	}
	*d = discount(strconv.FormatFloat(asNumber, 'f', -1, 64))
	return nil
}

// isFull reports whether the discount is 100%.
func (d discount) isFull() bool {
	cleaned := strings.TrimSpace(string(d))
	cleaned = strings.TrimPrefix(cleaned, "-")
	cleaned = strings.TrimSuffix(cleaned, "%")
	value, err := strconv.ParseFloat(strings.TrimSpace(cleaned), 64)
	if err != nil {
		return false
	}
	return value >= 100
}

// --- wire types -------------------------------------------------------------

type catalog struct {
	Products       []product `json:"products"`
	ProductCount   int       `json:"productCount"`
	CurrentlyShown int       `json:"currentlyShownProductCount"`
}

type product struct {
	ID              string `json:"id"`
	Slug            string `json:"slug"`
	Title           string `json:"title"`
	ProductType     string `json:"productType"`
	StoreLink       string `json:"storeLink"`
	CoverHorizontal string `json:"coverHorizontal"`
	CoverVertical   string `json:"coverVertical"`
	Price           struct {
		FinalMoney money    `json:"finalMoney"`
		BaseMoney  money    `json:"baseMoney"`
		Discount   discount `json:"discount"`
	} `json:"price"`
}

type money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Discount string `json:"discount"`
}
