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

## Behaviour
- **Identified sale:** staff (or a kiosk) select a present driver (by QR/`userId`) and items
  → publish `bar.order_placed {orderId, userId, items[], total, at}` (Billing adds to the
  tab).
- **Anonymous sale (Round 15):** a walk-in buys food/drinks with **no `userId`** → publish
  `bar.order_placed` with `userId` **absent/null**; Billing settles it immediately (no tab,
  no PII). An anonymous buyer who wants an invoice must declare it **before paying**.
- Every order carries a client-side `orderId` for Billing's inbox dedupe.
- **Includes a simulator** (like Timing) to generate orders during development/demos.

## Events
**Publishes** (`bar.events`): `bar.order_placed`.
**Consumes**: `identity.resolved` (resolve a walk-in to `userId` if needed),
`driver.checked_in` (know who's currently present).

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Order for a driver with no open tab | Still publish `bar.order_placed`; Billing opens/attaches a tab retroactively. |
| Order for an unknown person | Resolve via `identity.lookup_requested`; until then, hold the order against the raw token. |
| Duplicate submit (double-tap) | Each order has a client-side id; Billing's inbox dedupes. |
| RabbitMQ down | Orders persisted locally + queued in outbox; published on recovery so nothing is lost. |
| Restart | Local order store durable; unpublished orders flushed from the outbox. |
