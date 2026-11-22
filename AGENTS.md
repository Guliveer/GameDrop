# AGENTS.md

Instructions for AI coding agents (Claude Code, Codex, Cursor, Copilot, Gemini
CLI, Aider, Windsurf, Cline, and others) working in this repository. `CLAUDE.md`,
`GEMINI.md`, `CONVENTIONS.md`, `.github/copilot-instructions.md`,
`.cursor/rules/gamedrop.mdc`, `.windsurfrules`, and `.clinerules` summarize or
reference this file. If they disagree, this file wins.

## What this project is

GameDrop is a headless free-game collector written in Go. One process invocation
performs **one collection cycle**: load config, fetch Steam/Epic/GOG offers for
configured countries, persist them in SQLite, deliver Telegram notifications from
an outbox, log a summary, exit. Deployment: one Fly.io Scheduled Machine with one
Fly Volume.

There is no web server, no HTTP API, no UI, no accounts, and no in-app scheduler.
Do not add them.

## Commands

```bash
go run ./cmd/gamedrop        # one cycle (dev defaults: ./gamedrop.db, no Telegram)
./scripts/run.sh             # same, via the helper script (scripts\run.bat on Windows)
go build ./...               # compile everything
go test ./...                # full offline test suite
go test ./internal/storage -run TestApplyExpiration -v
gofmt -w . && gofmt -l .     # format; the list must be empty
go vet ./...
go mod tidy                  # keep go.mod/go.sum clean
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o gamedrop ./cmd/gamedrop
docker build -t gamedrop .   # needs a working container daemon
```

Before claiming work is done, run at least: `gofmt -l .`, `go vet ./...`,
`go test ./...`, `go build ./...`. `go test -race ./...` needs cgo + gcc; CI runs
it, a Windows host without a C compiler cannot.

## Repository map

```
cmd/gamedrop/main.go        wiring, exit codes (0 ok/partial, 1 failure, 2 bad config)
internal/config             env parsing + aggregated validation
internal/envfile            .env loader (no dependency, real env wins)
internal/logger             slog (text dev, JSON production)
internal/model              Offer/Store/OfferType/Availability/SourceStatus
internal/httpclient         shared retrying client (idempotent requests only)
internal/stores             Adapter interface + registry
  steam/ epic/ gog/ prime   independent adapters with testdata fixtures
internal/storage            SQLite schema, migrations, offers, history, runs, outbox
internal/notify             rendering, Telegram sender, outbox processor
internal/collector          cycle orchestration, concurrency, notification policy
docs/                       architecture, development, testing, stores, database,
                            notifications, configuration, operations, ADRs
```

## Non-negotiable rules

1. **One cycle per invocation.** Never add a scheduler, ticker, daemon loop, HTTP
   listener, or background worker.
2. **Standard library first.** The only third-party dependency is
   `modernc.org/sqlite` (pure Go, required for CGO-free static builds). Adding
   another dependency requires a clear justification and maintainer approval.
3. **Environment files are built in.** `internal/envfile` loads `.env` (or
   `ENV_FILE_PATH`) before configuration; real environment variables always win.
   Do not add a dotenv dependency or move file parsing into `config`.
4. **No PostgreSQL, no external queue, no microservices.** A modular monolith with
   SQLite is the design (see `docs/adr/`).
5. **Do not weaken classification.** `free_to_keep` requires a verified 100%
   discount from a non-zero original price. Never infer it from a zero price
   alone. Keep `unknown` when evidence is missing.
6. **Never invent deadlines.** Zero `time.Time` means unknown. Do not default
   `EndsAt`/`StartsAt` to now, the far future, or a fixed duration.
7. **Never delete or expire data because a fetch failed or an offer vanished.**
   Absence sets `missing_since`; expiration requires `ends_at` in the past.
8. **Identity is `(store, country, store_offer_id)`.** Never deduplicate by title,
   never merge regional variants.
9. **Secrets never enter the repository or logs.** No tokens, cookies, headers, or
   `.env` files. The HTTP client strips query strings from errors.
10. **Do not claim exactly-once notification delivery.** The outbox is
    at-least-once; document duplicates as a known trade-off.
11. **Do not change the deployment shape silently.** `fly.toml` defines no
    services; deployment uses `fly deploy --build-only --push` plus
    `fly machine update --skip-start`. A plain `fly deploy` would create an
    always-running Machine — never recommend it.
12. **English only** in code, comments, tests, docs, and commit messages.

## Change recipes

- **New storefront:** implement `stores.Adapter` in `internal/stores/<name>/`,
  keep endpoint URLs as fields for `httptest`, add fixtures + tests (success,
  empty, missing fields, malformed, unexpected structure, HTTP failure,
  country/locale parameters), register in `cmd/gamedrop/main.go`, add
  `ENABLE_<NAME>` config, update `.env.example` and `docs/`.
- **New config variable:** `internal/config/config.go` (field, `Load`, `Validate`)
  → `.env.example` → `docs/configuration.md` → test.
- **Schema change:** append a new migration in `internal/storage/db.go`; never
  edit an applied one; add a re-open test; update `docs/database.md`.
- **New notification kind:** decide in `collector.planner` with a unique dedupe
  key; render in `notify.RenderOffer`; test that a repeated scan does not
  re-enqueue it.
- **Bug in an adapter:** save the real payload as a fixture and write the failing
  test first, then fix parsing.

## Testing expectations

- Tests must be offline and deterministic: `httptest` + `testdata/` fixtures,
  injected clocks (`Now func() time.Time`), `t.TempDir()` databases.
- No `time.Sleep` for synchronisation, no live store or Telegram calls.
- Every schema or business-rule change needs a persistence test; every parsing
  change needs a fixture regression test.
- Prefer asserting stored state (offers, history, outbox) over log strings.

## Style

- `gofmt` clean, `go vet` clean, idiomatic Go.
- Wrap errors with context (`fmt.Errorf("steam: fetch featured: %w", err)`); log
  at the boundary that decides, not at every layer.
- Propagate `context.Context` through all I/O.
- Structured `slog` with stable keys (`run_id`, `source`, `country`, `offer_id`,
  `duration_ms`, `status`, `error`).
- No global mutable state; pass dependencies explicitly.
- Small focused packages; comments explain *why*, not *what*.

## Definition of done

Code compiles, tests pass, docs updated (`docs/`, README, `.env.example` when
configuration changed), no secrets or database files added, and every claim in the
final message reflects a command that was actually run. Do not commit, push, or
deploy unless explicitly asked.
