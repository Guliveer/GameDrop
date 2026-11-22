# CLAUDE.md

@AGENTS.md

The imported file is the authoritative instruction set for this repository. The
notes below are Claude Code specifics and do not override it.

## Quick start

```bash
go run ./cmd/gamedrop    # one collection cycle
go test ./...            # offline suite (fixtures, temp SQLite, mocked Telegram)
gofmt -l . && go vet ./...
```

## Working style for this repo

- Read `docs/architecture.md` before changing orchestration, and
  `docs/stores.md` before touching an adapter. `docs/database.md` describes the
  offer/outbox lifecycle and invariants.
- Prefer small, targeted edits. Do not restructure packages, rename files, or
  reformat unrelated code.
- Keep the deployment model intact: one-shot worker, one SQLite file on one Fly
  Volume, no server, no scheduler.
- When a store payload surprises you, reproduce with `curl`, save a fixture, and
  write the failing test before fixing the parser.
- Do not add dependencies. `modernc.org/sqlite` is the only one, and it is there
  to keep `CGO_ENABLED=0` builds working.
- Secrets: never read, print, or commit `.env`, tokens, chat IDs, or cookies.
  Local runs work without Telegram credentials.

## Verification before you report success

Run and report the real output of: `gofmt -l .`, `go vet ./...`,
`go test ./...`, `go build ./...`. Mention anything you could not run (for
example `go test -race` without a C compiler, or `docker build` without a daemon)
instead of assuming it passes.

Do not create commits, push branches, open pull requests, or deploy unless the
user explicitly asks.
