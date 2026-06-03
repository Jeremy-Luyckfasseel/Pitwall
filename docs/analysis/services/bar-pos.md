# Service: Bar / POS

> A thin point-of-sale producer for bar orders. Added because the brief mentions a bar
> but lists no bar service. Decisions: [Q&A](../00-questions-and-answers.md) Round 12.
> Conforms to the [service blueprint](../04-service-blueprint.md).

## Purpose
Let bar staff record orders against a driver, which Billing adds to that driver's tab.
The bar is simply another **event producer** on the bus.

## System of record (owns)
- Bar **orders** (items, totals, who, when) — its own record of what was sold.
- A simple **product/price list** (local).
- A **local read-model copy** (ECST) of **wallet / gift-card balances** (from Billing's
  `wallet.*` / `giftcard.*` events) so the POS can accept stored-value payment and validate
  gift-card codes without a synchronous call (Round 17, [ADR-0011](../../adr/0011-external-payments-edge.md)).

## Behaviour
- **Identified sale:** staff (or a kiosk) select a present driver (by QR/`masterId`) and items
  → publish `bar.order_placed {orderId, masterId, items[], total, at}` (Billing adds to the
  tab).
- **Anonymous sale (Round 15):** a walk-in buys food/drinks with **no `masterId`** → publish
  `bar.order_placed` with `masterId` **absent/null**; Billing settles it immediately (no tab,
  no PII). An anonymous buyer who wants an invoice must declare it **before paying**.
- **Pay from stored value (Round 17):** a sale can be settled from a **wallet** (present
  driver's `masterId`) or by **redeeming a gift-card code** — a **balance debit**, never a card
  charge. The POS marks the order as wallet/gift-card payment; **Billing** applies the debit
  and emits `wallet.debited` / `giftcard.redeemed`. **Partial** coverage falls back to normal
  POS payment for the remainder (no dead end). The exact spend-intent wiring is an
  implementation detail (ADR-0011 consequences).
- Every order carries a client-side `orderId` for Billing's inbox dedupe.
- **Includes a simulator** (like Timing) to generate orders during development/demos.

## Events
**Publishes** (`bar.events`): `bar.order_placed`.
**Consumes**: `identity.resolved` (resolve a walk-in to `masterId` if needed),
`driver.checked_in` (know who's currently present),
**`wallet.topped_up`/`wallet.debited`/`giftcard.issued`/`giftcard.redeemed`** (local balance
copy for stored-value payment + code validation, ADR-0011).

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Order for a driver with no open tab | Still publish `bar.order_placed`; Billing opens/attaches a tab retroactively. |
| Order for a buyer with no account | No raw-token-buffered tab (Round 19): handle as an **anonymous immediately-settled sale** (no `masterId`, no PII). To go on a tab — or get a formal invoice — the buyer must be a registered driver with a `masterId` (register-first). |
| Duplicate submit (double-tap) | Each order has a client-side id; Billing's inbox dedupes. |
| RabbitMQ down | Orders persisted locally + queued in outbox; published on recovery so nothing is lost. |
| Restart | Local order store durable; unpublished orders flushed from the outbox. |
| Pay from wallet but balance too low | Debit the available balance; charge the remainder by normal POS payment — no dead end. |
| Gift-card code invalid / expired / exhausted | POS validates against its local copy and Billing's ledger guard; reject gracefully (no double-spend, no "computer says no"). |
| Balance copy stale (just topped up online) | Billing's ledger is the SoT; the POS reconciles on the `wallet.*`/`giftcard.*` event and never over-debits (ledger guard is authoritative). |
