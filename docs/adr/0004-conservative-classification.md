# ADR 0004: Conservative offer classification

## Context

Store payloads are inconsistent: a zero price can mean a giveaway, a free
weekend, a permanently free product, a demo, or a regional pricing artifact. A
false "free to keep" alert erodes trust; a missed alert is recoverable.

## Decision

Only classify an offer as `free_to_keep` with explicit evidence: a verified 100%
discount from a non-zero original price (and, where available, a shop-specific
signal such as Steam's `appdetails` or Epic's `discountPercentage == 0`). If the
evidence is missing or contradictory, the offer is stored as `unknown` or skipped.
Deadlines are never invented; the zero `time.Time` means "unknown".

## Consequences

- Notifications are trustworthy; `unknown` offers are visible in the database for
  later adapter improvements.
- Some real giveaways may be missed until an adapter improves.
- Adapters need extra verification requests (Steam fetches `appdetails` for each
  candidate), which is why candidate counts are capped.
- Expiration is evidence-based: offers without a deadline stay `available` and
  only gain `missing_since`.
