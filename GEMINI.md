# GEMINI.md

Repository instructions for Gemini CLI and similar agents. The full, authoritative
rule set lives in [`AGENTS.md`](AGENTS.md); this file is a summary — when in
doubt, follow `AGENTS.md`.

## Project

GameDrop: a headless Go free-game collector. One invocation = one collection cycle
(fetch Steam/Epic/GOG → normalize → persist in SQLite → notify via Telegram
outbox → exit). Deployed as one Fly.io Scheduled Machine with one volume. No
server, no API, no UI, no in-app scheduler.

## Commands

```bash
go run ./cmd/gamedrop
go build ./...
go test ./...
gofmt -l . && go vet ./...
```

## Hard rules

- Only dependency: `modernc.org/sqlite`. Do not add others; `.env` support is
  built in (`internal/envfile`), with real environment variables taking
  precedence.
- `free_to_keep` only with verified 100% discount from a non-zero original price.
- Never invent deadlines; zero time means unknown.
- Never delete or expire offers because a store failed or an item disappeared.
- Offer identity is `(store, country, store_offer_id)`; never dedupe by title.
- No secrets in code, tests, logs, or files.
- Notifications are at-least-once via a transactional outbox; do not claim
  exactly-once.
- Do not turn this into an always-running service; scheduled Machines require
  `fly machine run --schedule`, not a plain `fly deploy`.
- Code, comments, tests, and docs in English.

## Verification

`gofmt -l .` (empty), `go vet ./...`, `go test ./...`, `go build ./...`. Report
checks you could not execute. Do not commit, push, or deploy unless asked.
