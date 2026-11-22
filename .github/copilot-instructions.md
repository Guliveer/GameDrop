# GitHub Copilot instructions

GameDrop is a headless Go free-game collector: one process invocation performs one
collection cycle and exits. Full repository rules are in
[`../AGENTS.md`](../AGENTS.md); the summary below is what matters for code
suggestions.

## Architecture in one paragraph

`cmd/gamedrop` wires config, logging, storage, adapters and the collector, then
exits. `internal/stores/*` are independent adapters (Steam, Epic, GOG, disabled
Prime) that fetch and classify offers for one country. `internal/collector` runs
adapters × countries with bounded concurrency, persists each result in one SQLite
transaction (`internal/storage`), expires offers with passed deadlines, and drains
the notification outbox (`internal/notify`). Notifications go to Telegram.

## When suggesting code

- Follow existing patterns: errors wrapped with `%w`, `context.Context` first,
  structured `slog`, table-driven tests with `httptest` and `testdata/` fixtures.
- Never suggest adding a scheduler, HTTP server, web UI, or background worker.
- Never add third-party dependencies. `modernc.org/sqlite` is the only one;
  `.env` loading is built in (`internal/envfile`), with real environment
  variables taking precedence over file entries.
- Keep `free_to_keep` classification conservative: require a verified 100%
  discount from a non-zero original price; otherwise use `unknown` or skip.
- Never invent `EndsAt`/`StartsAt`; the zero `time.Time` means unknown.
- Never delete or expire offers because they were absent from a scan or because a
  source failed.
- Offer identity is `(store, country, store_offer_id)`.
- Do not log or embed secrets (bot tokens, chat IDs, cookies, `.env` contents).
- Do not claim exactly-once notification delivery; the outbox is at-least-once.
- Keep documentation and comments in English.

## Tests

Prefer offline tests: `httptest` servers for adapters, fixture files under
`testdata/`, `t.TempDir()` databases, injected clocks. No live HTTP in tests.

## Verification

Suggest and rely on `gofmt -l .`, `go vet ./...`, `go test ./...`, `go build ./...`.
