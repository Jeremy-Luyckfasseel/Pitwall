# Service: Identity

> The sole issuer of the canonical `masterId`. Deliberately minimal. Decisions:
> [Q&A](../00-questions-and-answers.md) Rounds 3 & 5, [ADR-0003](../../adr/0003-identity-as-uuid-issuer.md).
> Conforms to the [service blueprint](../04-service-blueprint.md).

## Purpose
Guarantee that there is **exactly one canonical `masterId` (UUID) per person**, so every
service is provably talking about the same human. Nothing more.

## System of record (owns)
- `masterId` (canonical UUID).
- A **natural key** for de-duplication: **email** (optionally name/phone later).
- Status + timestamps (created/updated).

**Explicitly NOT owned:** passwords/credentials (Frontend), profile/contact (CRM),
racing data (Driver). **No fuzzy merging.**

## The one operation: resolve-or-mint
A service that needs a `masterId` publishes `identity.lookup_requested {requestId,
email}`. Identity:
1. Looks up the email.
2. **Known** → return the existing `masterId`. **Unknown** → mint a new UUID, store it.
3. Reply `identity.resolved {requestId, email, masterId}`.

There is **no `isNew` flag** — the new-vs-returning decision lives entirely inside
Identity; every consumer simply **idempotently upserts** on the returned `masterId`.
This is event-driven (no synchronous API), so it works within the bus-only rule.

The `masterId` is embedded into the **QR code** issued to the user, so Timing reads it
directly at the gate.

## Events
**Publishes** (`identity.events`): `identity.resolved`.
**Consumes**: `identity.lookup_requested` (from Frontend registration, the
walk-in kiosk/counter, etc.).

## Key flows
- **Online registration**: Frontend stores credentials locally, publishes
  `identity.lookup_requested {email}` → `identity.resolved {masterId}` → Frontend links
  the credential record; CRM/Driver upsert their records on the same `masterId`.
- **Walk-in**: counter/kiosk publishes `identity.lookup_requested {email}` → mint →
  QR printed with the `masterId`.

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Two lookups for the same new email race | Idempotent on email: the first mints, the second returns the same `masterId`. Single-writer/unique-constraint on email guarantees one UUID. |
| Lookup for an email that already exists | Returns the existing `masterId` (returning person) — no duplicate identity. |
| RabbitMQ down | Lookups queue; replies emitted via outbox when the bus recovers. Requesters wait on the reply event (their flow is async). |
| Malformed lookup (no/invalid email) | Validate against contract → log + dead-letter; emit no resolution. Requester's sad path handles the timeout. |
| Service restart | Stateless beyond its store; replays unprocessed lookups; the issued-id registry is durable. |
