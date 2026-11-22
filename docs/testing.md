# Testing

Tests are part of the deliverable. They run fully offline: no store API, no
Telegram, no credentials, no network access.

```bash
go test ./...
go test ./internal/storage -run TestApplyScan -v
go test -race ./...
go test ./... -cover
```

## Layers

| Layer | Packages | What it proves |
|---|---|---|
| Unit | `config`, `model`, `httpclient`, `stores` (registry) | Defaults, validation, normalization, identity, fingerprints, retry/timeout policy |
| Adapter | `internal/stores/{steam,epic,gog,prime}` | Parsing, classification, region parameters, malformed/unexpected payloads, HTTP failures |
| Persistence | `internal/storage` | Migrations, upserts, idempotency, history, missing-marking, expiration, reappearance, outbox, rollback |
| Integration | `internal/collector`, `cmd/gamedrop` | Whole cycles with mocked adapters and a temp SQLite file; exit codes |

Current statement coverage (local run): collector 89.8%, config 90.1%, model
87.5%, storage 78.0%, epic 86.2%, gog 88.2%, steam 86.7%, prime 100%,
notify 52.9%, httpclient 67.7%. Coverage is a signal, not a target: critical
business rules have explicit cases.

## Techniques used

- **`httptest` servers** instead of live endpoints. Adapters expose their
  endpoint URLs as fields (`FeaturedURL`, `FeedURL`, `CatalogURL`) so tests point
  them at a local server that serves `testdata/` fixtures.
- **Fixtures on disk** (`internal/stores/*/testdata/*.json`) keep production
  types responsible for parsing, so schema drift fails a test.
- **Injected clocks.** Adapters and the collector accept `Now func() time.Time`;
  storage takes `Options.Now`. No test sleeps to simulate time passing.
- **Fake adapters and senders** for cycle tests (`collector_test.go`): a fake
  `stores.Adapter` returns offers or errors, a fake `notify.Sender` records text
  and can fail on demand.
- **Temporary databases**: every persistence test opens its own file under
  `t.TempDir()`, so tests are independent and parallel-safe.
- **A trigger to force rollback**: `TestApplyScanRollsBackOnFailure` creates a
  SQLite trigger that aborts notification inserts, proving the offer insert is
  rolled back with it.

## What each file covers

| File | Highlights |
|---|---|
| `internal/config/config_test.go` | defaults, production DB path, country parsing/dedupe, Telegram auto-enable/mismatch, duration formats, validation matrix |
| `internal/model/offer_test.go` | normalization, contradictory windows, validation, identity, fingerprint stability, expiration/notifiability, money formatting |
| `internal/httpclient/client_test.go` | 429 + `Retry-After`, 503 exhaustion, no retry on 4xx, no retry on POST, User-Agent, context cancellation, query redaction |
| `internal/storage/storage_test.go` | migration idempotency, insert/update/unchanged, invalid offers, missing vs expired, reappearance, outbox dedupe/retry, rollback, run/source records, stats |
| `internal/stores/steam/steam_test.go` | verified giveaway vs permanent free vs unverifiable vs free weekend, DLC exclusion, empty/malformed/unexpected, HTTP failure, language mapping |
| `internal/stores/epic/epic_test.go` | current vs upcoming, permanently free/DLC/discounted exclusion, URL slug normalization, country/locale parameters, clock-dependent windows |
| `internal/stores/gog/gog_test.go` | client-side giveaway detection, pagination, empty page stop, partial result on later page failure, discount parsing |
| `internal/notify/notify_test.go` | Telegram success, 429/5xx/4xx classes, token never leaked, message rendering with/without deadline |
| `internal/collector/collector_test.go` | dedupe across cycles, failure isolation, all-fail status, deferred notification retry, upcoming→available, expiration, partial scans |
| `cmd/gamedrop/main_test.go` | exit code 2 for bad configuration, exit code 1 for storage failure |

## Rules for new tests

1. Deterministic: no real clock dependencies, no network, no `time.Sleep` for
   synchronisation.
2. One behaviour per test; the test name states the expected outcome
   (`TestMissingOffersAreNeverDeletedOrExpired`).
3. Prefer asserting stored state (`GetOfferByKey`, history, outbox) over asserting
   log output.
4. When fixing a bug, add the regression case to the relevant fixture or test
   first.
5. Keep fixtures small but include at least one malformed element.

## CI

`.github/workflows/ci.yml` runs, on pull requests and pushes to `main`:
`gofmt` verification, `go vet`, `go build`, `go test -race`, `govulncheck`, and a
Docker image build. `.github/workflows/deploy.yml` repeats the verification
before deploying.

Locally, `go test -race` needs cgo and a C compiler (`gcc`/`clang`). On Windows
without one, run the plain suite locally and rely on CI for the race build.
