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
- **Stored-value ledger** (Round 17, [ADR-0011](../../adr/0011-external-payments-edge.md)):
  the balances of **wallets** (keyed on `userId`) and **bearer gift cards** (by code) — one
  ledger primitive, Billing is its **system-of-record**.
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

## Stored value — wallets & gift cards (Round 17, [ADR-0011](../../adr/0011-external-payments-edge.md))
Billing owns the **stored-value ledger**. Online card payment (the **only** online card
operation) happens at the **Frontend payments edge**; Billing reacts to the money-in fact and
owns every **balance** fact:
- On **`payment.captured`** (from the edge): credit the balance and emit **`wallet.topped_up`**
  (purpose `wallet_topup`) or **`giftcard.issued`** (purpose `giftcard_purchase`). Keyed to
  `sourcePaymentId` for idempotency.
- **Spending is a balance debit on the bus**, never a fresh card charge: Billing applies a
  spend (at any POS, or online e.g. a booking from wallet) and emits **`wallet.debited`** /
  **`giftcard.redeemed`**. **Partial spend** supported; **insufficient balance** is not a dead
  end — the remainder falls back to normal POS payment. Redemption is **idempotent** (inbox +
  ledger guard): a code cannot be double-spent.
- **VAT = multi-purpose voucher (MPV):** **loading is non-taxable** (a payment-on-account, no
  VAT at sale); **VAT is accounted at spend/redemption** through the existing document logic
  above (FR61–65, FR81–82) — no parallel VAT engine.
- **Refund/expiry:** loaded value is **non-refundable** once captured (cancel only
  pre-capture); balances carry a **configurable, EU-compliant expiry**, handled per policy and
  **logged, never silently dropped**. **E-money/PSD2 is out of scope** — closed-loop facility
  under a **configurable balance cap**, no KYC/AML.

## Data governance ([ADR-0009](../../adr/0009-data-governance.md))
On `privacy.erasure_requested`, Billing **anonymizes** (not deletes) any invoice within the
**7-year** retention window: it keeps the number, amounts, VAT, and date but nulls
name/address/email/`userId`, writes a tombstone, and emits `privacy.erased {mode:
anonymized}`. Records outside retention are deleted. A **wallet with an outstanding balance
defers erasure** like an open tab (FR78) — erasure never destroys live financial value; once
settled/expired, the slice is anonymized under the financial-retention rule. **Bearer gift
cards carry no PII**. Stored-value ledger entries fall under the **7-year** financial window.

## Events
**Publishes** (`billing.events`): `tab.opened`, `tab.item_added`, `invoice.issued`,
**`wallet.topped_up`**, **`wallet.debited`**, **`giftcard.issued`**, **`giftcard.redeemed`**
(stored value, ADR-0011).
**Consumes**: `driver.checked_in`, `bar.order_placed`, `session.ended`,
`crm.company_updated`/`crm.employee_linked`/`crm.person_updated`,
**`payment.captured`** (from the Frontend payments edge → credit the ledger).

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Private person requests a formal invoice | Capture invoice details on demand, issue a proper numbered invoice — no company required. |
| `session.ended` arrives but no tab was opened | Open a tab retroactively from the check-in record (or a zero tab), then close it; log the anomaly. |
| Duplicate `bar.order_placed` (redelivery) | Inbox dedupe by message id → not double-charged. |
| CRM company data not yet known at close | Issue a receipt now; if company/billTo arrives later, support a re-issue/credit path rather than blocking. |
| Invoice-number sequence under restart | Sequence is durable + transactional → gapless, no reuse. |
| RabbitMQ / peer down | Tab state local; `invoice.issued` queued via outbox; PDF stored locally until deliverable. |
| Duplicate `payment.captured` (redelivery) | Dedupe on `sourcePaymentId` → balance credited once, no double top-up. |
| Spend exceeds wallet/gift-card balance | Debit only the available balance (partial); the remainder falls back to normal POS payment — no dead end. |
| Gift-card code invalid / exhausted / expired | Graceful reject (logged); no balance change, no "computer says no". |
| Erasure requested for a wallet with a balance | Defer like an open tab; anonymize once settled/expired (financial-retention rule). |
