# Architecture Decision Records

Short records of decisions that are expensive to reverse. Format: context,
decision, consequences. One file per decision, numbered and never rewritten —
supersede instead.

| ADR | Decision |
|---|---|
| [0001](0001-one-shot-worker.md) | One-shot worker instead of a long-running service |
| [0002](0002-sqlite-on-fly-volume.md) | SQLite on a Fly Volume with a pure-Go driver |
| [0003](0003-transactional-notification-outbox.md) | Transactional outbox, at-least-once delivery |
| [0004](0004-conservative-classification.md) | Conservative offer classification |
| [0005](0005-region-in-offer-identity.md) | Country is part of an offer's identity |
| [0006](0006-failure-isolation.md) | Per-source failure isolation with no data deletion |

When adding an ADR: copy the structure of an existing one, use the next number,
and add it to the table above.
