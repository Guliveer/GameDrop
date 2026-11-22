# GameDrop

GameDrop is a small, headless free-game collector written in Go. It discovers
games that are free for a limited time on Steam, the Epic Games Store and GOG,
stores them (with history) in SQLite, and sends a Telegram message when
something new appears.

It runs **one collection cycle per invocation**: load configuration, fetch every
enabled store for every configured country, normalize and classify offers,
persist them, notify, record the run summary, exit. There is no server, no public
API, no accounts, and no in-app scheduler. The scheduler is the deployment
platform — a Fly.io Scheduled Machine started by `fly machine run --schedule`.

## Features

- **Three storefront integrations** (Steam, Epic Games Store, GOG), each
  independent, with per-source timeouts and failure isolation.
- **Conservative classification**: Free-to-Keep requires explicit evidence
  (verified 100% discount against a non-zero original price). Zero price alone
  is never enough; unknown evidence stays `unknown`.
- **Region-aware**: each store is queried for explicitly configured countries;
  offers carry their country, and regional variants are separate records.
- **SQLite persistence** with migrations, offer history, per-source run status,
  collection-run history, and a notification outbox.
- **No data loss on flaky stores**: a missing offer is never deleted or expired
  just because it vanished from one scan; expiration requires a confirmed end
  date that has passed.
- **Telegram notifications** from a transactional outbox with bounded retries.
- **Structured logs** (`log/slog`) with a run ID and a final execution summary.

## Architecture

```
cmd/gamedrop          entry point, one cycle, exit codes
internal/config       env loading + validation
internal/logger       slog setup (text for dev, JSON for production)
internal/model        Offer, Store, OfferType, Availability, SourceStatus
internal/httpclient   shared HTTP client: pooling, timeouts, retries, Retry-After
internal/envfile     .env loader (built in, no dependency, real env wins)
internal/stores       adapter interface + registry
  ├── steam           featuredcategories + appdetails verification
  ├── epic            freeGamesPromotions feed (current + upcoming)
  ├── gog             catalog API (explicit 100% discount only)
  └── prime           documented disabled adapter
internal/storage      SQLite: offers, history, source state, runs, outbox
internal/notify       message rendering, Telegram sender, outbox processor
internal/collector    cycle orchestration, concurrency, failure isolation
scripts/run.sh        one-cycle helper (Linux/macOS)
scripts/run.bat       one-cycle helper (Windows)
```

Deployment: one Fly.io app, one Scheduled Machine, one Fly Volume mounted at
`/data` holding `offers.db`. No PostgreSQL, no queue, no microservices.

### How one cycle works

1. Load and validate configuration (fail fast, exit code 2 on bad config).
2. Open SQLite, apply migrations.
3. Fetch every enabled store × configured country, bounded by
   `MAX_CONCURRENCY`, each with its own `SOURCE_TIMEOUT_SEC`.
4. Persist each successful (or partially successful) source in **one
   transaction**: idempotent upserts, history entries, notification intents.
   Failed sources write nothing, so their last known data survives.
5. Expire offers whose confirmed end date has passed.
6. Deliver pending notifications from the outbox, recording outcomes.
7. Record the run summary and log one structured "cycle finished" line.
8. Exit: `0` success or partial success, `1` complete failure, `2` bad config.

## Supported stores

| Store | Source | Classification | Known limitations |
|---|---|---|---|
| Steam | `featuredcategories` + `appdetails` (per country) | `is_free=true` → permanent free; verified `initial>0, final=0, 100% off` → free-to-keep; still-priced free-weekend listing → `free_weekend`; otherwise `unknown` | No start date or explicit end date beyond the listing's `discount_expiration`; free weekend detection is a documented heuristic |
| Epic Games Store | `freeGamesPromotions` feed, per country/locale | running `discountPercentage=0` promo with non-zero original price → free-to-keep (available); same promo starting later → upcoming; DLC/bundles/etc. skipped; permanently free products skipped | Epic's Browse GraphQL endpoint is not used; the anonymous feed already provides price, window, and identifiers |
| GOG | `catalog.gog.com/v1/catalog`, trending discounted pages (server-side price filters are ignored, so 3 pages are scanned and filtered client-side) | only items whose final price is exactly 0 with a non-zero base price and a 100% discount; permanently free entries excluded | GOG exposes no reliable giveaway end date, so `EndsAt` stays unknown instead of being invented; a giveaway that never surfaces in the scanned trending pages can be missed |
| Prime Gaming | — | disabled adapter | Loot requires an authenticated Amazon session (cookies). Shipping cookie handling in a scheduled worker is unsafe and brittle, so the adapter reports a clear non-fatal error when enabled. Other sources are unaffected |

Adapters are independent: one failing store never prevents the others from being
persisted, and a failed store never erases its previous data.

## Regional behavior

The Fly.io region and the storefront region are separate settings.

- `STORE_COUNTRIES=PL` (default) queries every store for Poland.
- `STORE_COUNTRIES=PL,US` queries each store for both countries; each offer is
  stored under its own country, so a US-only promotion is never presented as
  available in Poland.
- `STORE_LOCALE=pl-PL` is passed where the store accepts a locale; Steam gets a
  mapped language (`polish`), Epic gets the locale, GOG gets the catalog locale.
- If a store cannot express regional availability, the offer records the country
  it was queried for, and the limitation is documented instead of guessing.

No US fallback is applied: the configured countries are exactly what is
requested.

## Configuration reference

All configuration is environment based. See [`.env.example`](.env.example) for a
complete, commented template.

| Variable | Default | Description |
|---|---|---|
| `APP_MODE` | `development` | `development` (text logs, `./gamedrop.db`) or `production` (JSON logs, `/data/offers.db`) |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `DATABASE_PATH` | mode dependent | SQLite file; parent directories are created |
| `HTTP_TIMEOUT_SEC` | `30s` | Per-request timeout (`30s` or `30`) |
| `HTTP_MAX_RETRIES` | `2` | Retries for idempotent requests (429/502/503/504, transport errors) |
| `HTTP_USER_AGENT` | `GameDrop/1.0 …` | User-Agent for outbound requests |
| `STORE_COUNTRIES` | `PL` | Comma separated ISO 3166-1 alpha-2 codes |
| `STORE_LOCALE` | `pl-PL` | Locale used where supported |
| `ENABLE_STEAM` | `true` | Enable the Steam adapter |
| `ENABLE_EPIC` | `true` | Enable the Epic adapter |
| `ENABLE_GOG` | `true` | Enable the GOG adapter |
| `ENABLE_PRIME` | `false` | Enable the (documented, disabled) Prime adapter |
| `SOURCE_TIMEOUT_SEC` | `45s` | Timeout for one store/country fetch |
| `MAX_CONCURRENCY` | `4` | Parallel store/country fetches |
| `TELEGRAM_BOT_TOKEN` | – | Bot token from @BotFather |
| `TELEGRAM_CHAT_ID` | – | Target chat ID |
| `ENABLE_TELEGRAM` | auto | Auto-on when both credentials exist; `true` requires them; `false` forces off |
| `NOTIFY_MAX_ATTEMPTS` | `3` | Delivery attempts per notification |

Configuration problems are reported together at startup, and the process exits
with code `2` without touching the database.

## Local development

Requires Go 1.27+.

```bash
cp .env.example .env   # loaded automatically at startup

./scripts/run.sh       # Linux/macOS
scripts\run.bat        # Windows

go run ./cmd/gamedrop  # equivalent, without the helper script
```

`.env` is parsed by the built-in `internal/envfile` loader: variables that
really exist in the environment take precedence, a missing file is fine, and
`ENV_FILE_PATH` selects a different file. Values such as `LOG_LEVEL=debug` can be
prefixed to a single run without editing the file.

The default development configuration uses `./gamedrop.db` and sends no
notifications unless Telegram credentials are provided. Each run prints a
summary and exits.

Build:

```bash
go build -o gamedrop ./cmd/gamedrop
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o gamedrop ./cmd/gamedrop
```

## Testing

```bash
gofmt -l .        # must print nothing
go vet ./...
go test ./...     # unit, adapter (fixtures), persistence, cycle integration
go test -race ./...
```

All tests run offline: store responses come from `testdata/` fixtures served by
`httptest`, Telegram is mocked, and every persistence test uses a temporary
SQLite file. No credentials are needed.

Coverage highlights: configuration validation, offer normalization/identity/
fingerprinting, expiration rules, deduplication, notification eligibility,
HTTP retry/timeout behavior, every adapter's success/empty/malformed/HTTP-failure
paths, migration idempotency, transaction rollback, failed-source preservation,
reappearance after expiration, and complete cycles with mocked stores.

## Docker

```bash
docker build -t gamedrop .
docker run --rm --env-file .env -v "$PWD/data:/data" gamedrop
```

The image is a multi-stage build: `golang:1.27-alpine` compiles a static binary
(`CGO_ENABLED=0`), and the runtime is `gcr.io/distroless/static-debian12:nonroot`
with CA certificates, running as non-root. No shell, no secrets, no source.

## SQLite storage

The database is a single file (`offers.db` in production) with WAL mode, a
busy timeout, and one writer connection (GameDrop is a single-process worker).

| Table | Purpose |
|---|---|
| `schema_migrations` | Applied migration versions (safe to re-run) |
| `offers` | Current state of every offer, unique on `(store, country, store_offer_id)` |
| `offer_history` | `new` / `updated` / `reappeared` / `expired` snapshots per run |
| `runs` | One row per collection cycle with counters and status |
| `source_runs` | Per-store/per-country outcome of each cycle (success/partial/failure) |
| `source_state` | Last known status, last success time, last error per source |
| `notifications` | Transactional outbox: `pending` → `sent` / `failed` with attempts |

Expiration rules, in order of precedence:

1. A confirmed `ends_at` in the past → `expired` (recorded once, with history).
2. An offer absent from a **fully successful** scan → marked `missing`
   (`missing_since`, `missed_scans`), never deleted or expired.
3. A failed or partial source → nothing is written at all.
4. An expired offer that reappears with an active window → `reappeared`,
   `expired_at` cleared, and it becomes notifiable again with a new dedupe key.

## Telegram setup

1. Create a bot with [@BotFather](https://t.me/BotFather) and copy the token.
2. Send `/start` to your bot.
3. Read the chat ID from `https://api.telegram.org/bot<TOKEN>/getUpdates`.
4. Set `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID`.

Messages contain the game title, store, offer type, store region, known claim
deadline, original price, and the store link. Messages are plain text (no parse
mode), so titles with `_` or `*` cannot break formatting.

Delivery semantics are **at-least-once**. Telegram's `sendMessage` has no
idempotency key, so a timeout after Telegram accepted a message can produce a
duplicate. Outbox rows record every attempt; transient failures are retried on
later runs with bounded backoff (1 min, 5 min, then every 30 min) until
`NOTIFY_MAX_ATTEMPTS` is reached. Deduplication is by intent: `new:<offer>`,
`now_available:<offer>:<deadline>`, `reappeared:<offer>:<timestamp>`.

## Fly.io deployment

Full runbook: [`docs/fly-deployment.md`](docs/fly-deployment.md).

```bash
fly apps create gamedrop
fly volumes create gamedrop_data --region fra --size 1 --app gamedrop
fly secrets set --app gamedrop TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=...
fly deploy --app gamedrop --build-only --push --image-label bootstrap
fly machine run registry.fly.io/gamedrop:bootstrap \
  --app gamedrop --region fra --schedule hourly --restart on-failure \
  --volume gamedrop_data:/data \
  --env APP_MODE=production --env DATABASE_PATH=/data/offers.db \
  --env STORE_COUNTRIES=PL --env STORE_LOCALE=pl-PL
```

Key points:

- `fly.toml` holds environment/mount/VM defaults and the Dockerfile reference.
  It defines no services: GameDrop exposes no ports.
- **Do not run a plain `fly deploy`.** It manages Fly Launch Machines, not a
  `fly machine run --schedule` Machine, and would create an always-running
  instance of a one-shot worker.
- Image updates: `fly deploy --build-only --push --image-label <tag>`, then
  `fly machine update <machine-id> --image registry.fly.io/gamedrop:<tag>
  --skip-start --yes`. This preserves the schedule, region, environment, and
  volume.
- Never pass `--rm` to the scheduled Machine, and never detach or destroy the
  volume during a deploy: it holds the only copy of SQLite.
- The volume is host-local and single-copy; scheduled starts are approximate;
  Fly does not alert on missed or failed runs. See the limitations below.

## GitHub Actions

- **CI** (`.github/workflows/ci.yml`): on pull requests and pushes to `main` —
  gofmt verification, `go vet`, `go build`, `go test -race`, `govulncheck`, and a
  Docker image build. No credentials required.
- **Deploy** (`.github/workflows/deploy.yml`): on pushes to `main` (and manual
  dispatch) — repeats the verification, pushes the image with
  `fly deploy --build-only --push`, then updates the Scheduled Machine image with
  `fly machine update --skip-start`. Serialized with a concurrency group,
  `main` only, never on fork pull requests.

Required: secret `FLY_API_TOKEN`; variables `FLY_APP_NAME` (optional) and
`FLY_MACHINE_ID` (needed for automatic Machine updates — otherwise the workflow
pushes the image and prints the exact manual command).

## Documentation

Full index: [`docs/README.md`](docs/README.md).

| Document | Contents |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | Component map, cycle data flow, failure model, design decisions |
| [`docs/development.md`](docs/development.md) | Bootstrap, local runs, quality gates, conventions, PR checklist |
| [`docs/testing.md`](docs/testing.md) | Test layers, fixtures, fake clocks, coverage, how to add tests |
| [`docs/stores.md`](docs/stores.md) | Adapter contract, per-store endpoints and rules, adding a store |
| [`docs/database.md`](docs/database.md) | Schema, indexes, invariants, migrations, lifecycles, backup |
| [`docs/notifications.md`](docs/notifications.md) | Rendering, outbox dedupe and retry policy, Telegram specifics |
| [`docs/configuration.md`](docs/configuration.md) | Every environment variable, defaults, validation matrix, examples |
| [`docs/operations.md`](docs/operations.md) | Log fields, run statuses, incident playbooks, monitoring |
| [`docs/fly-deployment.md`](docs/fly-deployment.md) | Fly app/volume/Machine runbook and recovery |
| [`docs/adr/`](docs/adr/README.md) | Architecture Decision Records |

### AI agent instructions

[`AGENTS.md`](AGENTS.md) is the authoritative rule set. Thin, tool-specific files
point to it: [`CLAUDE.md`](CLAUDE.md), [`GEMINI.md`](GEMINI.md),
[`CONVENTIONS.md`](CONVENTIONS.md) (Aider),
[`.github/copilot-instructions.md`](.github/copilot-instructions.md),
[`.cursor/rules/gamedrop.mdc`](.cursor/rules/gamedrop.mdc),
[`.windsurfrules`](.windsurfrules), and [`.clinerules`](.clinerules).

## Known limitations

- **Prime Gaming is not implemented.** Loot requires an authenticated Amazon
  session; the adapter is deliberately disabled and documented. Enabling it
  records a non-fatal source failure.
- **Scheduled execution is approximate.** Fly starts the Machine on a fuzzy
  interval; there is no in-app scheduler and no retry of a missed schedule.
- **Single volume, single Machine.** No replication, no high availability, and
  backups are a manual procedure.
- **Notification delivery is at-least-once**, not exactly-once.
- **Steam free weekends** are detected with a documented heuristic (advertised as
  free while the purchase price remains) because no public Steam JSON field
  states it directly.
- **Steam/GOG deadlines** may be unknown; GameDrop never invents them, so those
  offers are not auto-expired.
- **Epic upcoming offers** are discovered from the same feed; if Epic changes its
  anonymous feed shape the adapter fails cleanly and its last data is preserved.
- **GOG prices are filtered client-side.** The catalog API accepts but ignores
  `price=free`/`priceRange`, so GameDrop scans a bounded number of trending
  discounted pages (3 × 48 products). A giveaway that ranks outside those pages
  is missed until it becomes visible.
- **No historical price tracking** beyond offer snapshots, and no web UI.

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| Exit code 2 with "invalid configuration" | One or more env vars are wrong; the message lists all problems |
| `TELEGRAM_* is required when Telegram notifications are enabled` | `ENABLE_TELEGRAM=true` without credentials, or only one of the two is set |
| Exit code 1, "no configured source succeeded" | Every store failed (network/DNS/upstream). Stored offers are untouched; check logs for per-source errors |
| Status `partial` with exit code 0 | At least one store failed but the cycle completed. Look for `source failed` lines |
| `database is locked` | Another process is using the same SQLite file (two Machines on one volume). Use exactly one Machine |
| Notifications not arriving | Check `notifications` rows (`pending`/`failed`, `last_error`, `attempts`) and Telegram credentials |
| Same message twice | At-least-once delivery: a timeout after Telegram accepted the message |
| Offer missing from the store but still in the database | Working as intended: missing is not expiration. It expires only when its recorded deadline passes |
| Wrong region offers | Set `STORE_COUNTRIES`/`STORE_LOCALE`; the Fly region does not affect storefront queries |

## License

No license has been chosen for this repository yet.
