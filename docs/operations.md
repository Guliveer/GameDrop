# Operations

GameDrop runs unattended on a schedule. This document covers what to watch, what
the logs mean, and what to do when something fails.

## Logs

Development uses `log/slog` text output; production (`APP_MODE=production`) uses
JSON. Every line includes `app=gamedrop`, `mode`, and — for cycle work —
`run_id`.

| Message | Level | Key fields |
|---|---|---|
| `collection cycle started` | INFO | `countries`, `locale`, `sources` |
| `source scanned` | INFO | `source`, `country`, `status`, `duration_ms`, `offers_found`, `offers_new`, `offers_changed`, `offers_missing` |
| `source failed` | WARN | `source`, `country`, `status`, `duration_ms`, `error` |
| `source returned partial data` | WARN | `source`, `country`, `offers`, `error` |
| `adapter returned an invalid offer` | WARN | `source`, `country`, `error` |
| `offer expired` | INFO | `store`, `country`, `offer_id`, `title` |
| `notification delivered` | DEBUG | `notification_id`, `offer_id`, `kind` |
| `notification delivery failed` | WARN | `notification_id`, `attempts`, `retryable`, `error` |
| `collection cycle finished` | INFO | `status`, `duration_ms`, totals for sources, offers, notifications |
| `collection cycle failed` | ERROR | `status`, counters, `failures`, `error` |

Useful queries once logs are in a collector:

```
app=gamedrop status=partial
app=gamedrop msg="notification delivery failed"
app=gamedrop msg="source failed"
```

## Run outcomes

| Status | Meaning | Operator action |
|---|---|---|
| `succeeded` | Every enabled source returned data and persistence completed | None |
| `partial` | At least one source failed; the rest were persisted | Watch the trend: a persistently failing source is an adapter or upstream change |
| `failed` | No source succeeded, or storage failed | Investigate immediately; existing data is untouched |

Exit codes: `0` succeeded/partial, `1` failed, `2` invalid configuration.

## Monitoring checklist

There is no built-in alerting. If you want to be notified of silence, monitor
externally:

1. **Run presence.** A row in `runs` (or a "cycle finished" log line) at least
   once per expected interval. Missing runs usually mean the Machine is stopped
   or the host could not start it.
2. **Status distribution.** Repeated `partial`/`failed` runs indicate upstream
   API changes.
3. **Outbox backlog.** `notifications` rows with `state='failed'` and
   `attempts >= NOTIFY_MAX_ATTEMPTS`.
4. **Database size.** `offers.db` growing unexpectedly signals duplicate offers
   or missing dedupe (identity regression).

Fly: `fly logs --app gamedrop`, `fly machine status <id> --app gamedrop`.

## Playbooks

### All sources failed

Symptom: `collection cycle failed`, `ErrNoSourceSucceeded`, exit 1.

1. Read `failures` in the log line — it names store, country, and error.
2. If every store fails with a DNS/TLS error, suspect the network egress or
   system clock of the Machine.
3. If one store fails, run the same endpoint with `curl` (see
   [stores.md](stores.md)) to confirm the payload changed.
4. Data is safe: failed sources write nothing.

### One source keeps failing

1. Open a fixture-based reproduction: save the new response, add a test, then fix
   the adapter.
2. As a stopgap, disable that adapter (`ENABLE_STEAM=false`) — the other stores
   keep working.

### `database is locked`

1. Confirm exactly one Machine uses the volume: `fly machine list --app gamedrop`.
2. A second process (helper Machine, backup script) must never write concurrently.

### Outbox backlog / no Telegram messages

1. Check `notifications.last_error` for the failure class.
2. Fix credentials or chat ID; retry permanently failed rows by resetting them to
   `pending`.
3. `attempts` increments per run, so a transient outage drains over the next few
   scheduled runs.

### Offer still listed after it disappeared from the store

Working as designed. Disappearance only sets `missing_since`; expiration requires
a recorded deadline. If the store gives no deadline (Steam/GOG), the offer stays
`available` until it reappears with a new window or is expired manually.

### Time zone and deadlines

All timestamps are stored in UTC (unix seconds). Notification deadlines are
formatted in UTC with an explicit `UTC` suffix, so a Polish user sees the
deadline unambiguously.

## Backup routine

- Nightly (or weekly) copy of `offers.db` plus `-wal`/`-shm` off the volume.
- Verify a restore into a scratch SQLite file and run `PRAGMA integrity_check`.
- Keep backups outside the Fly app; a lost volume is not recoverable from Fly.

## Retention (current state)

There is no automatic pruning. `offers`, `offer_history`, `runs`, and
`source_runs` grow slowly. A retention job is listed as technical debt in
[database.md](database.md); until then, manual pruning is possible, for example:

```sql
DELETE FROM runs WHERE started_at < strftime('%s','now','-90 days');
DELETE FROM offers WHERE availability='expired' AND expired_at < strftime('%s','now','-365 days');
```

Run such statements only when no cycle is executing, and take a backup first.

## Change management

- Image updates: `fly deploy --build-only --push`, then
  `fly machine update <id> --image … --skip-start`. See
  [fly-deployment.md](fly-deployment.md).
- Configuration changes: update `fly.toml` `[env]` for non-secrets, `fly secrets
  set` for secrets. The next scheduled run picks them up.
- Schema changes ship with the image; migrations apply automatically on start.
