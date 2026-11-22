# Development guide

## Prerequisites

- Go 1.27 or newer (`go version`).
- No C compiler is needed: SQLite uses the pure-Go `modernc.org/sqlite` driver,
  so `CGO_ENABLED=0` builds work everywhere.
- Optional: Docker (for the image), `flyctl` (for deployment), `sqlite3` (for
  inspecting a local database).

## Bootstrap

```bash
git clone https://github.com/Guliveer/GameDrop.git gamedrop
cd gamedrop
cp .env.example .env   # GameDrop loads .env automatically at startup
```

Variables exported in the shell always win over `.env` entries, so temporary
overrides work as expected:

```bash
LOG_LEVEL=debug ./scripts/run.sh
```

Defaults are safe for development: `APP_MODE=development`, SQLite at
`./gamedrop.db`, Poland as the storefront country, Prime disabled, Telegram
disabled (no credentials).

## Run one cycle

```bash
./scripts/run.sh        # Linux/macOS
scripts\run.bat         # Windows (cmd or double-click)

go run ./cmd/gamedrop   # without the helper script
```

The scripts change into the repository root, warn when Go is missing, and pass
all arguments through to the binary. `.env` is loaded by the application itself,
so both entry points behave identically.

Expected output in development mode: a start line, one line per source, and a
`collection cycle finished` summary. Exit code is `0` for a successful or
partially successful cycle.

Useful variants:

```bash
# Only Epic, no notifications, temporary database
APP_MODE=development LOG_LEVEL=debug DATABASE_PATH=./tmp/offers.db \
ENABLE_STEAM=false ENABLE_GOG=false ENABLE_PRIME=false \
go run ./cmd/gamedrop

# Production-style JSON logs
APP_MODE=production LOG_LEVEL=info go run ./cmd/gamedrop
```

## Inspect the database

```bash
sqlite3 ./gamedrop.db
.headers on
.mode column
SELECT store, country, store_offer_id, title, offer_type, availability, ends_at FROM offers;
SELECT id, status, offers_seen, offers_new, notifications_sent FROM runs ORDER BY started_at DESC LIMIT 5;
SELECT kind, state, attempts, last_error FROM notifications;
```

Schema details and invariant queries: [database.md](database.md).

## Quality gates

Run these before every commit; CI enforces them.

```bash
gofmt -l .          # must print nothing
go vet ./...
go test ./...       # offline, fixture-based
go test -race ./... # requires cgo + gcc locally; CI always runs it
go build ./...
go mod tidy         # must produce no diff
```

`go test -race` needs a C toolchain: on Windows without gcc, rely on CI. On
Linux/macOS install gcc/clang.

## Code conventions

- **Errors:** wrap with context (`fmt.Errorf("steam: fetch featured: %w", err)`),
  return them to the caller, and only log at the boundary that owns the decision.
  No silent `_ =` on error-producing calls.
- **Context:** every I/O function takes `ctx` and passes it down. Adapters must
  honour cancellation and deadlines.
- **Logging:** structured `slog` with stable key names (`run_id`, `source`,
  `country`, `offer_id`, `duration_ms`, `status`, `error`). Never log tokens,
  cookies, request headers, or full URLs with query strings (the HTTP client
  redacts them in errors).
- **Time:** store UTC; treat the zero `time.Time` as "unknown", never as Unix
  epoch. Tests inject clocks instead of sleeping.
- **State:** no package-level mutable state. Dependencies are passed explicitly.
- **Formatting:** `gofmt` is mandatory; comments explain *why*, not *what*.
- **Language:** code, comments, tests, docs, and commit messages in English.

## Where to change what

| Task | Files |
|---|---|
| New storefront | `internal/stores/<name>/`, registry in `cmd/gamedrop/main.go`, docs in `docs/stores.md` |
| New configuration variable | `internal/config/config.go`, `.env.example`, `docs/configuration.md` |
| Schema change | `internal/storage/db.go` (append migration), `docs/database.md` |
| Notification text | `internal/notify/notify.go` |
| Notification policy (what is worth notifying) | `internal/collector/collector.go` (`planner`) |
| Retry/backoff behaviour | `internal/httpclient/client.go`, `internal/notify/processor.go` |
| Exit codes | `cmd/gamedrop/main.go` + `docs/architecture.md` |

## Adding a configuration variable

1. Add the field and default to `config.Config` / `Load`.
2. Add validation to `Validate` if an invalid value is unusable.
3. Document it in `.env.example` and `docs/configuration.md`.
4. Add a case to `internal/config/config_test.go` (defaults or validation).

## Adding a database migration

1. Append a new entry to `migrations` in `internal/storage/db.go` with the next
   version. Never modify an applied migration.
2. Keep statements idempotent (`IF NOT EXISTS`) and split them one statement per
   string.
3. Add a test that opens an existing database and re-opens it.
4. Update [database.md](database.md) and the migration summary in the README.

## Debugging a live store

Adapters are thin wrappers over public endpoints, so reproduce with `curl`
first:

```bash
curl -sS 'https://store-site-backend-static.ak.epicgames.com/freeGamesPromotions?locale=pl-PL&country=PL&allowCountries=PL' | head -c 2000
curl -sS 'https://store.steampowered.com/api/featuredcategories?cc=PL&l=polish' | head -c 2000
curl -sS 'https://catalog.gog.com/v1/catalog?countryCode=PL&locale=pl-PL&productType=in:game&discounted=true&limit=5&order=desc:trending' | head -c 2000
```

Then save the shape as a fixture (redact volatile fields) and write a
regression test before changing parsing logic. Endpoint notes and field quirks
are documented in [stores.md](stores.md).

## Pull request checklist

- [ ] `gofmt -l .` empty, `go vet ./...` clean, `go test ./...` green.
- [ ] New behaviour covered by a test (fixture-based, no network).
- [ ] Docs updated (`docs/`, README, `.env.example` if configuration changed).
- [ ] No new dependency without a clear justification.
- [ ] No secrets, tokens, cookies, or database files in the diff.
- [ ] Deployment shape unchanged, or [fly-deployment.md](fly-deployment.md) updated.
