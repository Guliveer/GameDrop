# Conventions

Coding conventions for GameDrop, used by Aider and other agents that read
`CONVENTIONS.md`. The authoritative repository rules are in
[`AGENTS.md`](AGENTS.md).

## Language and formatting

- Go 1.27, standard library first. `gofmt` output is mandatory.
- Code, comments, tests, documentation, and commit messages in English.
- Comments explain intent and non-obvious constraints, not restatements of code.

## Structure

- `cmd/` wires dependencies and owns exit codes; it contains no business logic.
- Adapters (`internal/stores/*`) parse and classify only. They never touch
  storage or notifications.
- `internal/storage` owns SQL and transactions; rendering is injected as a
  callback, never imported from `notify`.
- `internal/collector` is the only package that composes the others.

## Errors and context

- Return errors; wrap with context: `fmt.Errorf("gog: fetch catalog page %d: %w", page, err)`.
- Log once, at the boundary that decides the outcome (collector, main). Do not
  log-and-return in the same place.
- Every I/O function takes `context.Context` first and honours cancellation.
- No `panic` for expected failures; no ignored errors without an explicit comment.

## Time, identity, and money

- Store times in UTC; the zero `time.Time` means "unknown" — never epoch.
- Identity is `(store, country, store_offer_id)`, never the title.
- Money is integer minor units plus an ISO-4217 currency; `nil` means unknown.
- Tests inject clocks instead of sleeping.

## Logging

Structured `slog` with stable keys: `run_id`, `source`, `country`, `offer_id`,
`notification_id`, `duration_ms`, `status`, `error`. Never log tokens, cookies,
request headers, or URLs with query strings.

## Database

- One writer connection; WAL; transactions for every multi-statement write.
- Migrations are append-only, versioned, idempotent statements.
- No implicit deletion: absence marks `missing_since`; expiration needs a passed
  `ends_at`.

## Tests

- Offline only: `httptest`, `testdata/` fixtures, `t.TempDir()` SQLite files.
- Name tests after the expected behaviour; one behaviour per test.
- Add a regression test with every bug fix, using the real payload as a fixture.

## Commits

- A message describes what changed and why, in the imperative mood.
- No attribution, signatures, or trailers: no `Co-Authored-By`, no "Generated
  with …", no tool names, no emojis.

## Dependencies and configuration

- Adding a dependency requires justification; SQLite is the only external one.
- Configuration is environment-only, with an optional `.env` loaded by
  `internal/envfile` (real environment variables win): field in `config.Config`,
  parse in `Load`, validate in `Validate`, document in `.env.example` and
  `docs/configuration.md`.
