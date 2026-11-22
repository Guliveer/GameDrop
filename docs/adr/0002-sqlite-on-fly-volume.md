# ADR 0002: SQLite on a Fly Volume with a pure-Go driver

## Context

The dataset is small (hundreds of offers, their history, run and notification
records). The worker runs briefly and exits. Options: PostgreSQL (managed),
SQLite on a persistent volume, or an embedded key-value store.

## Decision

SQLite on a single Fly Volume mounted at `/data`, accessed through
`modernc.org/sqlite` (pure Go), with WAL mode and a single-connection pool.

## Consequences

- One deployable, no external database service, no connection pooling concerns,
  no credentials, no network hop. Transactions give the atomicity the outbox
  needs.
- `CGO_ENABLED=0` still works, so the binary is static and runs on distroless.
- The volume is single-host and single-Machine: no replication, no failover, and
  backups are manual. Two Machines must never share the file.
- A move to PostgreSQL would require a repository layer refactor and migration
  tooling; the schema is intentionally conventional to keep that feasible.
