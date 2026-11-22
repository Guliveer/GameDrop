# ADR 0003: Transactional outbox, at-least-once delivery

## Context

A discovered offer should produce a Telegram message. Sending directly from the
discovery path couples a network call to a database transaction and loses the
intent when Telegram is unavailable or the process dies.

## Decision

Record notification intents in a `notifications` table inside the same
transaction that persists the offer change. Deliver them afterwards from a
separate processor, with bounded retries across runs. Acknowledge that delivery
is at-least-once.

## Consequences

- Intent survives crashes, timeouts, and multi-run Telegram outages.
- Dedupe keys (`new:…`, `now_available:…`, `reappeared:…`) make repeated scans
  idempotent at the intent level.
- Telegram's `sendMessage` has no idempotency key, so an ambiguous timeout can
  duplicate a message. This is documented rather than papered over.
- The outbox is channel-agnostic: adding email or Discord means implementing
  `notify.Sender`, not changing the schema.
- Messages are rendered before enqueue, so a later renderer change does not
  retroactively alter queued text.
