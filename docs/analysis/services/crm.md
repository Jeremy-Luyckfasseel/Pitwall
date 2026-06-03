# Service: CRM

> Source of truth for the **commercial relationship**: persons, companies, contacts,
> consent, loyalty. Decisions: [Q&A](../00-questions-and-answers.md) Round 7.
> Conforms to the [service blueprint](../04-service-blueprint.md).

## Purpose
Model the human/commercial side of a customer — distinct from Driver's racing side —
and the company/B2B relationships that Billing and Mailing depend on.

## System of record (owns)
- **Person**: legal name, email, phone, address — keyed on `masterId`.
- **Company**: name, billing details (VAT, address), keyed on `companyId`.
- **Employee links**: person ↔ company, and **billTo** (who/what is invoiced to the
  company).
- **Consent**: marketing/communication consent flags.
- **Loyalty**: a simple points or tier notion.

## Events
**Publishes** (`crm.events`): `crm.person_updated`, `crm.company_updated`,
`crm.employee_linked`, `crm.consent_changed`.
**Consumes**: `identity.resolved` (create a person on a new `masterId`),
`session.ended`/`invoice.issued` (loyalty accrual), Frontend profile-edit intents.

## Why it matters to others
- **Billing** uses company + employee-link + billTo to route invoices to a company
  (vs an individual receipt).
- **Mailing** checks `marketingConsent` before any non-transactional email.

## Key flow
1. New `masterId` → CRM creates a person record (contact details filled from
   registration/profile intents).
2. Person linked to a company → `crm.employee_linked` (Billing learns billTo).
3. Consent toggled in Frontend → `crm.consent_changed` (Mailing respects it).

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Conflicting contact edit from two sources | CRM owns contact fields → CRM's write wins (SoT precedence); same-owner race → last-write-wins by timestamp+version; conflict logged. |
| Invoice request for a person with no company | Not an error: person stays an individual; Billing issues a personal invoice (see Billing sad-paths). |
| Consent unknown for a user | Default to **no marketing consent** (safe default); transactional mail still sends. |
| RabbitMQ / peer down | Reads from local store; updates queued via outbox. |
| Restart | Replay from last marker; idempotent upserts. |
