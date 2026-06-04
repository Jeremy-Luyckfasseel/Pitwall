# Service: Bar / POS

> The **front-of-house POS / counter** — bar orders **plus** on-site walk-in registration, track-time
> booking, and (pre)payment. Decisions: [Q&A](../00-questions-and-answers.md) Round 12 (bar) +
> **Round 22 / [ADR-0014](../../adr/0014-front-of-house-pos-counter.md)** (counter scope).
> Conforms to the [service blueprint](../04-service-blueprint.md).

## Purpose
The staffed counter where a walk-in is **registered**, **books & pays for track time**, and buys at the
**bar** — all as **event producers/consumers** on the bus (never an inter-service API). It issues
intents; Identity remains the sole id-issuer and Booking the sole capacity authority.

## System of record (owns)
- Bar **orders** (items, totals, who, when) — its own record of what was sold.
- A simple **product/price list** (local).
- A **local read-model copy** (ECST) of **wallet / gift-card balances** (from Billing's
  `wallet.*` / `giftcard.*` events) so the POS can accept stored-value payment and validate
  gift-card codes without a synchronous call (Round 17, [ADR-0011](../../adr/0011-external-payments-edge.md)).
- A **local availability read-model** (ECST) of the **schedule** (from Booking's `session.scheduled` /
  `session.rescheduled` / `session.cancelled`) so the counter shows current session availability and
  keeps working (stale-flagged) when Booking or the bus are down (Round 22).

## Behaviour
### Counter — register, book, pay for track time (Round 22, ADR-0014)
- **Register a walk-in:** the counter captures an email (+ optional name) → publishes
  `identity.lookup_requested {requestId, email}`; on `identity.resolved` it binds the `masterId` to the
  **QR/transponder issued at the counter**. Register-first (FR39) holds; the POS **never mints a
  `masterId`**.
- **Book track time:** the counter shows live availability from its schedule read-model and books the
  driver into a chosen session → publishes `booking.requested {requestId, masterId, sessionId}`; Booking
  decides (capacity authority) and the counter reflects `booking.confirmed` / `booking.rejected`
  (+ alternatives — no dead end).
- **Pay for the session (incl. prepay):** the booked session can be **paid up front** at the counter by
  card/cash terminal or **wallet balance**, modelled with the **existing** Billing primitives (an
  immediately-settled tab charge / `wallet.debited`) — **no new money event** — *in addition to* the
  existing postpaid tab-at-session-end path. Insufficient wallet → partial split / card-cash fallback.

### Bar sales
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
