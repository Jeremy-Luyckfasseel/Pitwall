# ADR-0011 — The external payments edge (online stored value)

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Round 17

## Context
The analysis + early PRD phases kept **all payment at the POS** and declared online payment
a non-goal (PRD §8, OQ-4): Pitwall recorded charges and issued documents, while the actual
card/cash transaction happened at the terminal, never modelled on the bus.

The UX phase (2026-06-01) added two customer-facing surfaces — **gift cards/vouchers** and,
implied by them, **online payment** — and deliberately logged them as a scope extension to
feed back here. Selling a voucher (or topping up an account) online means **taking money
online**, which reopens that boundary. The tension: an online **PSP** (payment service
provider) is a **synchronous, external** integration, and Pitwall forbids inter-service APIs
/ synchronous RPC ([ADR-0002](./0002-event-carried-state-transfer.md),
[ADR-0008](./0008-single-bus-down-api-exception.md)).

## Decision
Introduce online payment in a **single, narrow funnel** and reconcile it with the bus-only
rule by recognising the boundary that already existed.

**1. The bus-only rule governs comms *between Pitwall services*, not Pitwall↔outside-world.**
Pitwall already crosses external edges synchronously: Frontend⇄browsers (HTTP), POS→**VIES**
(VAT validation, FR82), Mailing→SMTP. A **PSP is a fourth external edge** — categorically
different from inter-service RPC. (Contrast ADR-0008, which *is* a sanctioned bus-down RPC
*exception*; this is **not** an exception — it is an outbound call to a third party plus that
third party's inbound webhook.)

**2. A thin payments edge, inside Frontend, is the sole speaker of PSP HTTPS.** No new
service (CLAUDE.md keeps the count at 10 + Control Room). The PSP **webhook is treated as
external inbound** — the outside world calling in, exactly like a browser request or a VIES
reply — **not** a message between Pitwall services. Bus-only is therefore **preserved, not
excepted**. The v1 provider is **Mollie** (Belgian/EU-native: Bancontact/iDEAL/cards), behind
a provider **port** so it stays swappable. Card data never touches Pitwall (hosted checkout /
redirect → **PCI scope SAQ-A**).

**3. Online card payment exists for exactly one operation: loading stored value.** Top up a
**wallet** (keyed on the canonical `masterId`) or buy a **bearer gift card** (a redeemable code,
no PII). Both are one **stored-value ledger primitive owned by Billing**. The edge emits
**`payment.captured`** to `frontend.events`; Billing (ledger system-of-record) credits the
balance and emits **`wallet.topped_up`** / **`giftcard.issued`** to `billing.events`. The
capture→event step is **outbox-buffered** (ADR-0005), so a confirmed payment is never lost
when the bus is down — the customer still gets their balance/code, and the ledger reconverges
on reconnect.

**4. All spending is a balance debit on the bus — never a fresh online card charge.** Stored
value is spent at any POS (track time, food) and online (e.g. a booking paid from wallet).
Billing applies the spend and emits **`wallet.debited`** / **`giftcard.redeemed`** to
`billing.events`. Partial spend is supported; insufficient balance falls back to normal POS
payment (no dead end). Redemption is idempotent (inbox + ledger guard): a code cannot be
double-spent.

**5. VAT = multi-purpose voucher (MPV).** Because stored value buys supplies at potentially
different VAT rates, **loading is a non-taxable payment-on-account** (no VAT at sale); **VAT
is accounted at spend** through Billing's existing document logic (FR61–65, FR81–82).

**6. E-money / PSD2 is out of scope.** Spendable stored value can trigger EU e-money/PSD2
duties (KYC/AML, safeguarding) above thresholds. For this portfolio build, stored value is a
**closed-loop facility under a configurable balance cap**, no KYC/AML — a documented
limitation, not a production-compliant wallet.

## Consequences
- **New contract events** (`/contract`): `payment.captured` (frontend.events);
  `wallet.topped_up`, `wallet.debited`, `giftcard.issued`, `giftcard.redeemed`
  (billing.events). Balance-fact ownership sits with **Billing** as ledger SoT — this refines
  FR85's shorthand (which attributes the top-up emission to the edge): the edge owns the
  *payment* fact, Billing owns the *balance* facts.
- **Frontend** gains the payments edge (PSP port + webhook receiver + outbox) and an account
  read-model of wallet balance. **Billing** gains the stored-value ledger + MPV handling.
  **Bar/POS** gains "pay from wallet / redeem code" against its local balance copy.
- The **spend request** path (how a POS/online action asks Billing to debit) reuses the
  existing touchpoint flows; the precise spend-intent wiring (e.g. a small
  `wallet.payment_requested` intent vs. an order-flow flag) is an implementation detail left
  to the architecture phase.
- Defined **sad paths**: PSP unreachable/timeout at load = no value created (no value without
  confirmed capture, M10); duplicate webhook = idempotent no-op; bus-down at capture =
  outbox-buffered; invalid/exhausted/expired code = graceful reject; refund/expiry failure =
  dead-letter + alert.
- **Refunds/expiry:** loaded value is non-refundable once captured (cancel only pre-capture);
  balances carry a configurable, EU-compliant expiry; stored value falls under the **7-year
  financial retention** window, and a wallet balance **defers erasure** like an open tab.
