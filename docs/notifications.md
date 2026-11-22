# Notifications

Telegram is the only channel in v1. Delivery is driven by a transactional
outbox, not by direct sends from the discovery path.

## Why an outbox

A notification is a business event ("this offer is worth telling the user
about"). Creating that intent in the same SQLite transaction as the offer change
means a Telegram outage, crash, or timeout cannot lose it. Delivery is a separate,
retryable step.

```
ApplyScan (transaction)
   ├── upsert offer
   ├── insert offer_history
   └── insert notifications(state='pending', dedupe_key=..., payload=...)
                    │
                    ▼
        Processor.Run (after commit)
          pending / retryable failed rows
             │             │
          success       transient failure
             │             │
        state='sent'   state='failed', attempts++, next_attempt_at
```

## What is worth a notification

`collector.planner` runs inside the scan transaction and decides:

| Situation | Kind | Dedupe key |
|---|---|---|
| Newly seen, currently available, `free_to_keep` or `free_weekend` | `new` | `new:<store>:<country>:<offer id>` |
| Previously `upcoming`, now `available` | `now_available` | `now_available:<key>:<ends_at unix or 0>` |
| Expired offer observed active again | `reappeared` | `reappeared:<key>:<run timestamp>` |

Not notified: `permanent_free`, `unknown`, upcoming offers, unchanged
observations, expired offers, and anything already expired at discovery time.
When Telegram is disabled, no intents are created at all.

Because the dedupe key is unique, repeated scans cannot duplicate a message.
A changed deadline produces a new `now_available` key only if the offer actually
transitioned from upcoming; ordinary edits never re-notify.

## Message content

Plain text (no `parse_mode`), so titles containing `_`, `*`, or `[` cannot break
formatting:

```
🎮 Free game on Epic Games Store

Some Game
Offer: Free to keep
Store region: PL
Price: free (was 99.99 PLN)
Claim by: Fri, 27 Nov 2026 16:00 UTC

https://store.epicgames.com/pl/p/some-game
```

Unknown deadlines are stated explicitly (`Claim by: unknown (check the store
page)`); the URL is omitted when a store exposes none. Link previews are
disabled to keep messages compact.

## Delivery semantics

- **At-least-once.** Telegram's `sendMessage` has no idempotency key. A timeout
  after Telegram accepted a message can produce a duplicate. GameDrop records the
  attempt either way and does not claim exactly-once delivery.
- **One attempt per run per message**, with retries across runs:
  `attempts` increments, `next_attempt_at` is set to now + 1 min, then + 5 min,
  then + 30 min (or the server's `retry_after` when larger). Rows stop being
  retried at `NOTIFY_MAX_ATTEMPTS` (default 3).
- **Failure classes.** 429 and 5xx (plus network errors) are retryable; 4xx
  (bad token, wrong chat, blocked bot) are permanent and recorded with
  `last_error` for the operator.
- **Rate limiting.** Consecutive sends are spaced at least one second apart
  inside the process, matching Telegram's per-chat guidance.
- **Bounded batch.** At most 50 rows per run (`Processor.Limit`) to keep a
  backlog from extending the cycle indefinitely.

## Configuration

```bash
TELEGRAM_BOT_TOKEN=123456:ABC...
TELEGRAM_CHAT_ID=987654321
# optional: ENABLE_TELEGRAM=true|false, NOTIFY_MAX_ATTEMPTS=3
```

Rules:

- Both credentials set → notifications enabled automatically.
- `ENABLE_TELEGRAM=false` → disabled even with credentials.
- `ENABLE_TELEGRAM=true` without credentials → configuration error (exit 2).
- Neither set → the cycle runs silently and stores no intents.

Never put the token in `fly.toml`, the image, or the repository. On Fly use
`fly secrets set`. The token is never logged; errors include only Telegram's
description.

## Operating the outbox

```sql
-- Backlog and last error
SELECT id, state, attempts, next_attempt_at, last_error FROM notifications WHERE state != 'sent';

-- Messages for one offer
SELECT n.id, n.kind, n.state, n.attempts FROM notifications n
JOIN offers o ON o.id = n.offer_id
WHERE o.store = 'epic' AND o.store_offer_id = '<id>';
```

To retry a permanently failed message after fixing credentials, reset it:

```sql
UPDATE notifications SET state = 'pending', attempts = 0, next_attempt_at = NULL
WHERE id = <id>;
```

## Adding a channel

Implement `notify.Sender`:

```go
type Sender interface {
	Name() string
	Send(ctx context.Context, msg Message) error
}
```

Return a `*notify.DeliveryError{Retryable: true/false, RetryAfter: …}` so the
processor can schedule retries correctly. The outbox schema is channel-agnostic;
add a `channel` column in a new migration only when more than one channel must
run concurrently.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `notify: telegram: status 401` | Invalid bot token |
| `status 400: chat not found` | Wrong chat ID, or the bot has never been messaged by that user |
| `status 403: bot was blocked by the user` | Recipient blocked the bot |
| `rate limited (retry after N)` | 429; retry is scheduled automatically |
| Duplicate message | At-least-once semantics after an ambiguous timeout |
| No messages at all | Telegram disabled (no credentials), no eligible offers, or all intents already `sent` |
