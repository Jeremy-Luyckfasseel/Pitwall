# Service: Billing

> Owns all financial transactions: tabs, session charges, bar orders, and the
> generation of invoices/receipts. Decisions:
> [Q&A](../00-questions-and-answers.md) Round 9. Conforms to the
> [service blueprint](../04-service-blueprint.md).

## Purpose
Track what each driver owes during their visit and produce the correct financial
document (receipt for individuals, invoice for companies, or a formal invoice for an
individual on request) when the session ends.

## System of record (owns)
- **Tabs**: opened on check-in, items accrued, closed on session end.
- **Charges**: session charges + bar orders.
- **Invoices/receipts** and a **gapless sequential invoice-number** sequence.
- Local copies (ECST) of the bits of CRM it needs (company/billTo, contact) to address
  documents.

## Tab lifecycle
1. `driver.checked_in` → **open a tab** (`tab.opened`).
2. `bar.order_placed` → add bar items; session charge added too (`tab.item_added`).
3. `session.ended` → **close the tab**, decide document type, assign an invoice number,
   render a **PDF**, publish `invoice.issued {invoiceId, number, userId|companyId,
   documentRef, total}` for Mailing to deliver.

## Document type & invoicing — POS channel (Round 15)
- Individual, no company → **receipt** by default (anonymous, no buyer details).
- Linked to a company (via CRM `employee_linked` + billTo) → **company invoice**.
- **Invoices are requested at the POS** (not the Frontend), for **any** purchase — food,
  drinks, a session, the whole visit. A formal invoice legally requires **name + address**
  (VAT **only if a business**); the POS may **validate/enrich** a business buyer via the
  external **VIES** service — best-effort, **manual fallback**, never on the critical path.
- For an **anonymous sale** the invoice must be **declared before payment** (no key links a
  later request) — see below.
- **Re-issue:** if a document was already issued and a different one is later needed, Billing
  issues a **credit/void + a new document**, never a silent duplicate — preserving the
  gapless ledger. **No dead end.**

> **Anonymous POS sale** (`bar.order_placed` with **no `userId`**): recorded as an
> **immediately-settled** charge with **no tab** and **no PII**, still assigned a gapless
> document number — receipt by default, invoice only if declared before payment.

## Data governance ([ADR-0009](../../adr/0009-data-governance.md))
On `privacy.erasure_requested`, Billing **anonymizes** (not deletes) any invoice within the
**7-year** retention window: it keeps the number, amounts, VAT, and date but nulls
name/address/email/`userId`, writes a tombstone, and emits `privacy.erased {mode:
anonymized}`. Records outside retention are deleted.

## Events
**Publishes** (`billing.events`): `tab.opened`, `tab.item_added`, `invoice.issued`.
**Consumes**: `driver.checked_in`, `bar.order_placed`, `session.ended`,
`crm.company_updated`/`crm.employee_linked`/`crm.person_updated`.

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Private person requests a formal invoice | Capture invoice details on demand, issue a proper numbered invoice — no company required. |
| `session.ended` arrives but no tab was opened | Open a tab retroactively from the check-in record (or a zero tab), then close it; log the anomaly. |
| Duplicate `bar.order_placed` (redelivery) | Inbox dedupe by message id → not double-charged. |
| CRM company data not yet known at close | Issue a receipt now; if company/billTo arrives later, support a re-issue/credit path rather than blocking. |
| Invoice-number sequence under restart | Sequence is durable + transactional → gapless, no reuse. |
| RabbitMQ / peer down | Tab state local; `invoice.issued` queued via outbox; PDF stored locally until deliverable. |
