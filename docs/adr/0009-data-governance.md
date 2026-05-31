# ADR-0009 — Data governance & privacy as a first-class concern

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Round 16 ·
Amends Q0.1

## Context
The analysis phase ([Q0.1](../analysis/00-questions-and-answers.md)) deferred GDPR-style
compliance. The PRD phase reversed that: Pitwall handles driver PII (identity, contact,
racing history, financial documents) spread across many services, each holding a local
slice via ECST. Distributed PII with no lifecycle is a liability even for a portfolio
system, and the EU/Belgium VAT context makes retention and erasure concrete.

## Decision
Treat **data governance & privacy as first-class**, realised the same way as everything
else in Pitwall — **event-driven, choreographed, no new service**.

- **Retention** (configurable): financial/invoices **7 years** (Belgian accounting law);
  operational/racing data **while active + 2 years**; raw scan/transponder logs **90 days**.
- **Erasure** (right-to-be-forgotten): Frontend publishes `privacy.erasure_requested
  {userId}`; **every service** deletes (or, for Billing under legal retention,
  **anonymizes** — keep invoice number/amounts/VAT/date, null the PII) its local slice and
  emits `privacy.erased {userId, service, mode}`. The **Control Room** (existing aggregator)
  tracks completion. An erased `userId` becomes a **tombstone** so replayed/late events
  cannot resurrect it. Erasure **defers** while a tab is open / session active.
- **Export/portability**: `privacy.export_requested` → each service emits
  `privacy.data_provided {userId, service, payload}` → Control Room assembles →
  `privacy.export_ready` → Mailing delivers.
- **Audit**: append-only trail of consent/erasure/export actions, carrying `correlationId`
  **and the acting identity** (driver or admin actor).
- **Minimization**: each service stores only the slice it needs (already ECST), now an
  explicit requirement.

## Consequences
- No 11th service: governance reuses the bus, ECST, and the Control Room's aggregator role
  (a deliberate, documented stretch of its remit).
- The choreographed erasure/export saga introduces new contract events (`privacy.*`) and a
  per-service erasure handler in the [service blueprint](../analysis/04-service-blueprint.md).
- Billing carries the one retention exception (anonymize, don't delete) for tax compliance;
  the tombstone rule closes the ECST replay-resurrection hole.
- Eventual consistency applies to erasure too: completion is confirmed across services
  asynchronously, surfaced on the Control Room dashboard.
