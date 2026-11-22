# ADR 0001: One-shot worker instead of a long-running service

## Context

GameDrop needs to poll storefronts periodically. Options: an always-running
service with an internal scheduler (cron library, ticker), or a process that
performs exactly one cycle and exits, with the platform triggering it.

## Decision

One process = one collection cycle, then exit. Repetition is delegated to Fly
Scheduled Machines (`fly machine run --schedule hourly`). No scheduler code, no
daemon, no long-lived goroutines.

## Consequences

- The application has no timers, no drift, no "was the scheduler running?"
  failure mode, and no need for graceful shutdown beyond context cancellation.
- Each run starts from a known state; a crash affects only that run.
- Scheduling is approximate and up to the platform; a missed schedule is not
  retried by the app.
- Local development and tests invoke a cycle directly, without clock mocking at
  the process level.
- Any future move to a different platform requires only an external trigger (cron
  job, Cloud Scheduler, Kubernetes CronJob) — the binary stays unchanged.
