# Architecture

GameDrop is a modular monolith: one Go binary, one process, one collection cycle
per invocation, one SQLite file.

## Runtime model

```
Fly Scheduled Machine (cron-like, fuzzy interval)
        │
        ▼  exec /gamedrop
┌───────────────────────────────────────────────┐
│ 1. config.Load + Validate        (exit 2)     │
│ 2. logger.Init + storage.Open   (exit 1)      │
│ 3. fetch stores × countries (bounded parallel)│
│ 4. persist per source (transaction)           │
│ 5. expire offers with passed deadlines        │
│ 6. drain notification outbox                  │
│ 7. record run summary, log, exit 0/1          │
└───────────────────────────────────────────────┘
```

There is no server, no public port, no background daemon, and no in-app
scheduler. Fly starts the Machine, the process runs to completion, and Fly starts
it again on the next schedule. This keeps the state machine trivial: no
long-lived goroutines, no lock files, no graceful-shutdown drain beyond context
cancellation.

## Package map

```
cmd/gamedrop        wiring + exit codes; no business logic
  └── collector     orchestration, concurrency, isolation, notification policy
        ├── stores  adapter contract + registry
        │     ├── steam / epic / gog / prime   (independent adapters)
        ├── storage SQLite: offers, history, sources, runs, outbox
        ├── notify  message rendering, Telegram sender, outbox processor
        ├── model   domain types shared by every package
        └── httpclient   retrying HTTP client used by adapters and Telegram
```

Dependency rules:

- `model` depends on nothing but the standard library.
- Adapters depend on `model`, `stores`, and `httpclient`. They never touch
  `storage` or `notify`.
- `storage` depends on `model` only. Rendering is injected as a callback.
- `notify` depends on `model`, `storage`, and `httpclient` (outbox processor).
- `collector` is the only package that knows all of them; `cmd` knows only
  `collector`.

This keeps adapters replaceable and persistence testable without HTTP.

## One cycle in detail

1. **Configuration.** `config.Load` reads env, applies defaults, and normalizes
   `STORE_COUNTRIES`. `Validate` collects every problem and returns them in one
   error. A bad environment never reaches the database.
2. **Storage.** `storage.Open` creates parent directories, opens SQLite with
   `journal_mode=WAL`, `busy_timeout`, `foreign_keys=ON`, `synchronous=NORMAL`,
   and limits the pool to one connection (single writer). Migrations run in
   versioned transactions; re-running is a no-op.
3. **Discovery.** The collector expands enabled adapters × configured countries
   into jobs, runs them through a semaphore (`MAX_CONCURRENCY`), and gives each
   job its own context deadline (`SOURCE_TIMEOUT_SEC`).
4. **Persistence.** For each success/partial result, one transaction performs:
   validation and normalization of each offer, upsert by
   `(store, country, store_offer_id)`, history rows for real changes,
   notification intents (deduplicated by key), and missing-marking for offers
   absent from a *fully successful* scan.
5. **Expiration.** A separate transaction expires offers whose confirmed
   `ends_at` has passed. Nothing else can expire an offer.
6. **Notifications.** The outbox processor loads pending and retryable rows and
   delivers them one at a time, recording `sent`, `failed`, or a deferred retry
   with `next_attempt_at`.
7. **Reporting.** The run row is finalized with counters and status, and one
   structured summary line is logged.

## Failure model

| Situation | Persistence | Run status | Exit code |
|---|---|---|---|
| All enabled sources succeed | applied | `succeeded` | 0 |
| At least one source succeeds, others fail | applied for successes only | `partial` | 0 (logged as a warning) |
| Every source fails | nothing written; existing data untouched | `failed` | 1 |
| Storage/DB error | transaction rolled back | `failed` (best effort) | 1 |
| Invalid configuration | nothing opened | — | 2 |

Per-source outcomes are recorded in `source_runs` (success/partial/failure with
duration and error) and summarized in `source_state`.

## Offer lifecycle

```
                 ┌──────────── seen in a successful scan ────────────┐
                 ▼                                                   │
   (new) ──► available ──ends_at passes──► expired ──seen active──► reappeared
                 │  ▲                        │
                 │  └── missing_since set ◄──┘ (absent, deadline unknown)
                 ▼
              upcoming ──promotion starts──► available (notification: now_available)
```

- Absence only sets `missing_since`/`missed_scans`.
- Expiration requires a recorded deadline in the past.
- Reappearance clears `expired_at` and uses a new notification dedupe key.

## Concurrency

- Adapter fetches run in parallel; persistence is serialized by the SQLite
  single-writer design. This is intentional: the workload is network-bound, not
  write-bound.
- The collector waits for all fetches (`WaitGroup`) before persisting, so a slow
  store delays the cycle only up to its own timeout.
- The SQLite pool is capped at one connection; no lock contention exists inside
  the process.

## Design decisions (summary)

| Decision | Rationale |
|---|---|
| One-shot worker instead of a long-running service | Matches Fly Scheduled Machines; no scheduler code, no drifting timers; a crash only affects one cycle |
| SQLite on a Fly Volume | Single small dataset, no external service, transactional guarantees; pure-Go driver keeps `CGO_ENABLED=0` |
| Modular monolith | One deployable, one log stream, shared model; adapters are packages, not services |
| Transactional outbox | Notification intent cannot be lost when Telegram is unavailable |
| Conservative classification | False "free to keep" alerts are worse than a missed one; unknown evidence stays `unknown` |
| Country inside offer identity | Regional variants must never be merged or cross-advertised |
| Adapters never write to storage | Isolation of parsing from persistence; adapters stay unit-testable with fixtures |

Full rationale and consequences are recorded in [`adr/`](adr/README.md).

## Extension seams

- **New storefront:** implement `stores.Adapter` and register it in
  `cmd/gamedrop/main.go` (see [stores.md](stores.md)).
- **New notification channel:** implement `notify.Sender`; the outbox and
  processor are channel-agnostic (see [notifications.md](notifications.md)).
- **Schema change:** append a versioned migration; never edit a released one
  (see [database.md](database.md)).
- **New configuration:** add a field to `config.Config`, parse it in `Load`,
  validate it, document it in `.env.example` and `docs/configuration.md`.

## Non-goals

No web UI, no accounts, no claim automation, no price history charts, no
horizontal scaling, no message broker, no PostgreSQL. Adding any of these would
change the deployment shape documented in
[fly-deployment.md](fly-deployment.md).
