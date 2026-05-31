# ADR-0003 — Identity is a pure canonical-UUID issuer

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Rounds 3 & 5

## Context
With a database-per-service and polyglot stack, services must be certain they're
referring to the same person. An early proposal had Identity do "resolution/merging";
the user simplified it. UUIDs are unique by construction, but *canonicality* (one id
per person, issued by one authority) is what's actually needed.

## Decision
A dedicated **Identity** service is the **sole issuer of the canonical `userId`
(UUID)** — exactly one per person — and stores only `userId` + a natural key (**email**)
+ status/timestamps. Resolve-or-mint is **event-driven**: `identity.lookup_requested
{email}` → `identity.resolved {userId}` (mint if email unknown, reuse if known). **No
`isNew` flag**, **no passwords** (those stay in Frontend), **no profile** (CRM/Driver),
**no fuzzy merging**. The `userId` is embedded directly in the QR code.

## Consequences
- Every service joins on one agreed `userId`; consumers idempotently upsert.
- New-vs-returning logic lives in exactly one place (Identity).
- The brief's "conflicting driver data" edge case is **not** solved here; it moves to
  source-of-truth precedence + field-level last-write-wins (see
  [Q&A](../analysis/00-questions-and-answers.md) Q10.3).
- Walk-ins get a `userId` at first contact (counter/kiosk), so there is no anonymous
  state to merge later.
