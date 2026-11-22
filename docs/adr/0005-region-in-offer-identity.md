# ADR 0005: Country is part of an offer's identity

## Context

Store promotions are regional. A US-only giveaway must not be presented to a
Polish user, and the same store offer may differ per country (price, window,
availability). Country is configured independently from the Fly region.

## Decision

Offer identity is `(store, country, store_offer_id)`, with a matching UNIQUE
constraint. Adapters always receive the queried country and pass it to the store.
Notifications include the store region.

## Consequences

- Adding a country requires no schema change: it adds rows, not columns.
- The same game genuinely available in two countries produces two offers and two
  possible notifications; that duplication is the price of correctness.
- Regional availability is explicit; nothing is inferred from the server's IP
  geolocation.
- Stores that cannot express regions store `''` and expose that uncertainty
  rather than pretending.
