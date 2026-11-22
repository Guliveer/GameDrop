# Database

SQLite via the pure-Go `modernc.org/sqlite` driver. One file: `DATABASE_PATH`
(`/data/offers.db` in production on a Fly Volume). No other datastore is used.

## Connection settings

| Setting | Value | Why |
|---|---|---|
| `journal_mode` | `WAL` | Concurrent readers, crash safety, good throughput for one writer |
| `busy_timeout` | 5s (configurable in `storage.Options`) | Avoids spurious `SQLITE_BUSY` when a second process briefly opens the file |
| `foreign_keys` | `ON` | `offer_history` and `notifications` cascade on offer deletion |
| `synchronous` | `NORMAL` | Correct with WAL, much faster than `FULL` |
| pool size | 1 connection | Single-process worker; one writer removes contention entirely |

## Schema (migration v1)

### `offers`

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | surrogate key for FKs |
| `store` | TEXT | `steam`, `epic`, `gog`, `prime` |
| `country` | TEXT | uppercase ISO code, `''` if the store cannot be regionalized |
| `store_offer_id` | TEXT | canonical store identifier |
| `title`, `description`, `url`, `image_url` | TEXT | normalized, trimmed |
| `offer_type` | TEXT | `free_to_keep`, `free_weekend`, `permanent_free`, `unknown` |
| `availability` | TEXT | `available`, `upcoming`, `expired`, `unknown` |
| `price_minor`, `price_currency` | INTEGER, TEXT | NULL when unknown |
| `original_minor`, `original_currency` | INTEGER, TEXT | NULL when unknown |
| `starts_at`, `ends_at` | INTEGER | unix seconds, NULL when unknown |
| `source` | TEXT | e.g. `steam:featuredcategories+appdetails` |
| `fingerprint` | TEXT | SHA-256 of content fields; change detection |
| `first_seen_at`, `last_seen_at`, `last_verified_at` | INTEGER | bookkeeping |
| `missing_since`, `missed_scans` | INTEGER | absence tracking, never deletion |
| `expired_at` | INTEGER | set only when a confirmed deadline passed |

Constraints: `UNIQUE (store, country, store_offer_id)`.
Indexes: `offers_store_idx (store, country)`, `offers_availability_idx`,
`offers_ends_at_idx`.

### `offer_history`

`id`, `offer_id` (FK → `offers.id`, ON DELETE CASCADE), `run_id`, `change`
(`new`|`updated`|`reappeared`|`expired`), `snapshot` (JSON), `recorded_at`.
Indexed by `(offer_id, recorded_at)` and `run_id`. Unchanged observations are not
recorded, so history stays meaningful and small.

### `runs`

One row per cycle: `id` (run ID), `started_at`, `finished_at`, `status`
(`succeeded`|`partial`|`failed`), source counters, offer counters, notification
counters, `error`.

### `source_runs` / `source_state`

`source_runs` records every adapter attempt (source, country, status,
`offers_found`, `duration_ms`, `error`, timestamps). `source_state` keeps the last
known status per `(source, country)` plus `last_success_at` and `last_error`, so a
failed source is visible without scanning history.

### `notifications` (outbox)

`id`, `offer_id` (FK), `run_id`, `kind`, `dedupe_key` (**UNIQUE**), `payload`
(rendered text), `state` (`pending`|`sent`|`failed`), `attempts`, `last_error`,
`next_attempt_at`, `created_at`, `sent_at`, `updated_at`.

### `schema_migrations`

`version`, `name`, `applied_at`. Highest version is exposed by
`DB.SchemaVersion`.

## Invariants

1. Only `ApplyScan` writes offers, and only inside one transaction.
2. An offer absent from a fully successful scan is marked missing; it is never
   deleted and never expired.
3. A failed or partial source writes nothing at all.
4. Expiration happens only through `ExpireConfirmed` and requires
   `ends_at IS NOT NULL AND ends_at <= now`.
5. Reappearance clears `expired_at` and is recorded in history.
6. Notification intent is created in the same transaction as the offer change.
7. Repeated scans never create duplicate offers or duplicate intents.

## Offer lifecycle

| Event | Row effect | History |
|---|---|---|
| first observation | insert, `availability` from adapter | `new` |
| identical re-observation | `last_seen_at`, `last_verified_at`, missing cleared | — |
| content change | row updated, new fingerprint | `updated` |
| absent from a full scan | `missing_since`, `missed_scans++` | — |
| deadline passed | `availability='expired'`, `expired_at` | `expired` |
| expired offer seen active again | row updated, `expired_at=NULL` | `reappeared` |

## Migrations

- Declared in `internal/storage/db.go` as an ordered slice of
  `{version, name, statements}`.
- Applied in a transaction each, recorded in `schema_migrations`; re-running is a
  no-op.
- **Never edit an applied migration.** Append a new version.
- Statement strings are executed one at a time; prefer `IF NOT EXISTS`.
- `TestMigrationIsIdempotent` opens, closes, and re-opens the same file.

## Useful queries

```sql
-- Currently available offers, soonest deadline first
SELECT store, country, title, ends_at FROM offers
WHERE availability IN ('available','upcoming') AND (ends_at IS NULL OR ends_at > strftime('%s','now'))
ORDER BY COALESCE(ends_at, 0);

-- Offers missing from the last scan but not expired
SELECT store, country, store_offer_id, title, missing_since, missed_scans
FROM offers WHERE missing_since IS NOT NULL AND availability != 'expired';

-- Outbox backlog
SELECT id, state, attempts, next_attempt_at, last_error FROM notifications
WHERE state != 'sent' ORDER BY id;

-- Source health
SELECT source, country, status, datetime(last_success_at,'unixepoch'), last_error FROM source_state;

-- Last runs
SELECT id, status, started_at, finished_at, offers_seen, offers_new, notifications_sent
FROM runs ORDER BY started_at DESC LIMIT 10;
```

## Backup and recovery

- The database is small (a few MB). Copy `offers.db` **together with**
  `offers.db-wal`/`-shm`, or stop writes and run `VACUUM INTO 'backup.db'`.
- On Fly: use a temporary helper Machine with the volume attached (see
  [fly-deployment.md](fly-deployment.md)).
- Corruption: restore the last backup. The database is state, not source; it can
  also be rebuilt by re-running cycles, at the cost of history and dedupe memory.

## Known limitations / technical debt

- **No pruning.** `offers`, `offer_history`, and `runs` grow without bound.
  Expected growth is small (hundreds of offers, a few history rows per change),
  but a retention policy (e.g. delete expired offers older than a year and runs
  older than 90 days) is a candidate follow-up.
- **No compaction.** WAL checkpoints happen automatically; a periodic
  `PRAGMA optimize`/`VACUUM INTO` is not scheduled.
- **Single writer, single host.** Two Machines must never share one file.
