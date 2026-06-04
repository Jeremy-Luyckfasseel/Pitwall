# ADR-0014 — Front-of-house POS / on-site counter

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Round 22 ·
Originated in the `bmad-correct-course` workflow (2026-06-04), triggered by a system-map review.

## Context
A real karting venue has a **staffed front counter** that does everything in one place: registers a
walk-in, sells and books track time, takes payment, and runs the bar. Pitwall originally scoped
**Bar/POS to the bar only** (food/drink — it published just `bar.order_placed`), and decomposed the
counter experience across three services with **no single counter surface**:

- registration → kiosk/counter → Identity (register-first, FR39),
- pick a session + reserve → Booking (capacity authority, FR23),
- payment → a tab opened at check-in, settled **postpaid** at session end (Billing, FR59–61).

Reviewing the system map surfaced the gaps: Bar/POS consumes **no** session events and cannot sell
track time, and there is **no on-site walk-in flow** to register + book + pay for a session in one
interaction (booking was modeled as online-only; session payment as postpaid).

## Decision
**Bar/POS *is* the front-of-house POS / counter.** Beyond bar sales it now also:

1. **Registers walk-ins on-site.** The counter captures an email (+ optional name), publishes
   `identity.lookup_requested`, and on `identity.resolved` binds the `masterId` to the QR/transponder
   it issues. **Register-first (FR39) and sole-issuer (FR1, ADR-0003) are preserved** — the POS never
   mints a `masterId`. *(Decided: the POS owns counter registration rather than delegating to Timing.)*
2. **Sells & books track time.** It keeps a **local availability read-model** (consuming
   `session.scheduled`/`session.rescheduled`/`session.cancelled`) and books a walk-in into a session by
   publishing `booking.requested`. **Booking remains the single capacity authority (FR23, FR25)** — the
   counter only issues the intent and reflects `booking.confirmed`/`booking.rejected` (+ alternatives).
3. **Takes session payment, including prepay.** A booked session can be **paid up front** at the
   counter (card/cash terminal or wallet balance) **in addition to** the existing postpaid tab path.
   *(Decided: prepay reuses existing Billing primitives — an immediately-settled tab charge /
   `wallet.debited` — so there is **no new money event**.)*
4. **Keeps bar sales** (identified + anonymous, FR49–52) unchanged.

**Naming unchanged:** the service stays `bar-pos` and owns the `bar.events` exchange; only its
documented role broadens. **No new contract event *types*** — the counter reuses
`identity.lookup_requested`, `booking.requested`, `session.*`, and the tab/wallet/invoice primitives;
Booking and Identity already bind those routing keys (now also from `bar.events`).

## Consequences
- **Bus-only preserved.** Every counter action is a bus event; the POS is not an inter-service API and
  does not breach ADR-0001/0002. Register-first (FR39), capacity-authority (FR23), and sole-issuer
  (ADR-0003) all hold.
- **Bar/POS gains** an availability read-model and producer/consumer roles for identity + booking +
  session events. The system map and `02` event catalog are updated; **no new schemas**.
- **A new staff-facing POS/counter UX surface** exists — **stubbed for now** (like the admin UI), to be
  designed in its own run; it inherits the shared DESIGN.md identity.
- **Epics:** folded into Epic 7 ("Bar/POS (front-of-house counter) & Tab Lifecycle") as additive
  stories — same service, no file-churn split. The build had not started, so there is no rework.
- **Prepay is a Billing reuse, not a new path** — the same gapless-numbering / VAT machinery accounts a
  prepaid session exactly as any other immediately-settled charge.
- **Open (build-time):** the exact counter UX, and whether a prepaid-then-cancelled session needs a
  credit/void flow (covered in principle by FR64's credit/void + FR90 refund posture).
