# ADR 0006: Per-source failure isolation with no data deletion

## Context

Third-party storefront APIs break, rate-limit, and change shape. A cycle that
treats upstream trouble as "the offer no longer exists" would erase valid data and
re-notify everything later.

## Decision

Each adapter fetch has its own timeout and its failure is recorded per source
(`success`/`partial`/`failure`). Failed and partial sources write nothing. Absence
from a successful scan only marks offers missing. Deletion is not part of the
normal lifecycle.

## Consequences

- One broken storefront cannot destroy or duplicate data from the others.
- A partially successful cycle still exits `0`; a fully failed cycle exits `1` and
  leaves the database untouched.
- Stale offers can linger after a store stops listing them; `missing_since` and
  `missed_scans` make that visible, and confirmed deadlines still expire normally.
- Cleanup requires an explicit operator action or a future retention job, never an
  implicit delete on a failed fetch.
