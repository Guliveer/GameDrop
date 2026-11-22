# Store adapters

Every storefront is an independent adapter. Adapters fetch and classify; they
never write to the database, never notify, and never know about each other.

## Contract

```go
type Query struct {
	Country string // uppercase ISO 3166-1 alpha-2, e.g. "PL"
	Locale  string // e.g. "pl-PL"
}

type Adapter interface {
	Store() model.Store
	// Fetch discovers offers for one country. Returning offers together with an
	// error is a partial success and is recorded as such.
	Fetch(ctx context.Context, q Query) ([]model.Offer, error)
}
```

The collector runs adapters × countries with a semaphore and a per-job timeout,
then persists each result:

| Fetch outcome | Persisted | Run status contribution |
|---|---|---|
| `err == nil` | yes, and absent offers are marked missing | success |
| offers and `err != nil` | yes, but nothing is marked missing | partial |
| `err != nil`, no offers | nothing (previous data preserved) | failure |

## Invariants every adapter must honour

1. **Never invent evidence.** A zero price alone is not a giveaway. If the
   payload does not prove a 100% discount from a non-zero original price, the
   offer is `unknown` or is skipped — never `free_to_keep`.
2. **Never invent deadlines.** Leave `EndsAt`/`StartsAt` zero when the store does
   not provide them. The lifecycle depends on that distinction.
3. **Canonical IDs.** Use the store's own product/offer identifier, never the
   title, and never build synthetic IDs.
4. **Country is part of identity.** Set `Country` to the queried country, use
   country/locale parameters in every request, and do not merge regions.
5. **Bounded work.** A single fetch must fit inside `SOURCE_TIMEOUT_SEC`; cap
   verification fan-out (Steam batches, GOG pages).
6. **No side effects.** No storage, no logging of secrets, no global state.
7. **Tolerant parsing.** Unknown fields are ignored; unknown enumerations fall
   back to `unknown`; a malformed element is skipped, not fatal.

## Per-store details

### Steam (`internal/stores/steam`)

- Discovery: `GET /api/featuredcategories?cc=<country>&l=<language>`; candidates
  are items with `final_price == 0` and `discount_percent == 100`.
- Verification: `GET /api/appdetails?appids=<id,...>&cc=&l=&filters=basic,price_overview`,
  up to 8 IDs per request, at most 40 candidates.
- Locale: the primary subtag is mapped to Steam's language names (`pl-PL` →
  `polish`, `en-US` → `english`, …).
- Classification:

  | Evidence | Result |
  |---|---|
  | `data.type != "game"` | skipped (DLC, demo, video) |
  | `is_free == true` | `permanent_free` |
  | `initial > 0 && final == 0 && discount_percent == 100` | `free_to_keep` |
  | `final > 0` while the listing advertised zero | `free_weekend` (documented heuristic) |
  | `success == false` or no price overview | `unknown` |

- Deadline: the listing's `discount_expiration`, when present.
- Limitation: Steam publishes no start date, so upcoming offers cannot be
  reported; free weekends have no dedicated JSON field.

### Epic Games Store (`internal/stores/epic`)

- Source: `GET https://store-site-backend-static.ak.epicgames.com/freeGamesPromotions?locale=&country=&allowCountries=`.
- Free evidence: Epic expresses 100% off as `discountSetting.discountPercentage == 0`.
- Windows: a promotion containing now → `available`; a future promotion →
  `upcoming`; both carry `startDate`/`endDate`.
- Skipped: `originalPrice == 0` (permanently free), excluded `offerType`s
  (`DLC`, `ADD_ON`, `CONSUMABLE`, `SOUNDTRACK`, `VIRTUAL_CURRENCY`, `WALLET`,
  `DEMO`, `BUNDLE`), elements without ID or title.
- URLs prefer `catalogNs.mappings[].pageSlug`, then `productSlug` (with a
  trailing `/home` stripped), then `urlSlug`.
- The Browse GraphQL endpoint is intentionally not used: it requires unsigned
  persisted queries that change without notice, and the feed above already
  carries price, window, and identifiers.

### GOG (`internal/stores/gog`)

Realities of the public catalog API that shaped this adapter:

- Price filters (`price=free`, `priceRange`) are **accepted but ignored** by the
  server, so filtering happens client-side.
- Amounts are decimal strings; `discount` is a string such as `"-100%"`; the
  original price field is `baseMoney` (not `initialMoney`).
- `currencyCode` must be sent explicitly or prices come back in USD.

Strategy: scan up to `MaxPages` (default 3) trending **discounted** pages of
`productType=in:game`, 48 products each, and accept a product only when
`finalMoney.amount == "0"` **and** `baseMoney.amount > 0` **and**, when provided,
the advertised discount is 100%.

- Permanently free products (base price 0) are excluded.
- No end date exists in the payload, so `EndsAt` stays zero by design.
- Limitation: a giveaway that never appears in the scanned trending pages is
  missed. Raise `MaxPages` to scan deeper (at the cost of more requests).

### Prime Gaming (`internal/stores/prime`)

Deliberately disabled. Loot requires an authenticated Amazon session
(cookies/csrf tokens) against undocumented endpoints. Shipping cookie storage or
a headless browser in a scheduled worker is a security liability and would fail
silently. The adapter exists so enabling `ENABLE_PRIME=true` produces a clear,
non-fatal, isolated source failure instead of breaking the cycle.

## Adding a store

1. Create `internal/stores/<name>/<name>.go` with an `Adapter` implementing the
   contract and a `New(client *httpclient.Client) *Adapter`.
2. Keep endpoint URLs as fields so tests can point them at `httptest` servers,
   and inject a clock if the classification depends on "now".
3. Add `internal/stores/<name>/testdata/*.json` fixtures captured from the real
   endpoint (redacted, stable).
4. Write tests covering: success, empty payload, missing required fields,
   malformed JSON, unexpected structure, HTTP failure, country/locale parameters,
   and at least one regression case.
5. Add `ENABLE_<NAME>` to `internal/config` (default `false` unless proven), the
   registry in `cmd/gamedrop/main.go`, `.env.example`, `docs/configuration.md`,
   and the table in `docs/stores.md` and the README.
6. Verify with `go test ./internal/stores/<name>/ -v`; network use in tests is
   forbidden.

## Fixture policy

- One file per endpoint response, checked in under `testdata/`.
- Remove volatile data (timestamps that change, session IDs, personalized
  prices) or pin the clock in the test instead.
- Keep at least one "nasty" element: missing title, unknown enum, negative
  price, DLC type. Regression fixtures should encode real bugs previously found.
- Fixtures are parsed by the production types, so a schema drift in the adapter
  shows up as a failing decode test rather than a silent production failure.
