# Service: Mailing

> Sends automated email. **Reacts only** — never sends on its own initiative.
> Decisions: [Q&A](../00-questions-and-answers.md) Round 9. Conforms to the
> [service blueprint](../04-service-blueprint.md).

## Purpose
Turn meaningful events into the right email at the right time, while respecting
consent and never sending duplicates.

## Triggers → emails
| Event | Email |
|---|---|
| `booking.confirmed` | Booking confirmation |
| `session.ended` | Session summary with lap times |
| `personal_record.broken` | Personal-best alert |
| `invoice.issued` | Invoice/receipt delivery (with the document) |
| `session.rescheduled` / `session.cancelled` | Schedule-change notice |
| `alert.raised` (from Control Room) | Ops alert |
| `privacy.export_ready` (from Control Room) | Personal-data export delivery (ADR-0009) |

## Behaviour
- **Reacts only** to events; has no self-initiated scheduling.
- **Consent-aware**: transactional mail (booking, invoice, session summary, alerts)
  **always sends**; marketing/non-transactional mail checks CRM `marketingConsent`
  first (defaults to *no* if unknown). Skipped marketing mail emits `email.suppressed`.
- **Idempotent**: a `(recipient, template, sourceEventId)` key prevents duplicate sends
  on redelivery/replay.
- **Templated** with localisation-ready templates; logs every send (`email.sent`).
- **Delivery**: real SMTP in production; a local mail-catcher (e.g. Mailhog) in dev so
  nothing leaves the machine.

## System of record (owns)
- Send log / audit (what was sent, to whom, when, from which event).
- Local copies (ECST) of contact + consent needed to address and gate emails.

## The bus-down exception
When RabbitMQ is down, the Control Room calls Mailing via a **single sanctioned direct
API** purely to send the "bus is down" alert. This is the only non-bus entry point in
the system and is used for nothing else. See
[ADR-0008](../../adr/0008-single-bus-down-api-exception.md).

## Events
**Publishes** (`mailing.events`): `email.sent`, `email.suppressed`.
**Consumes**: the trigger events above; `crm.consent_changed`/`crm.person_updated`.

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| No marketing consent | Suppress the marketing mail, emit `email.suppressed`; transactional mail still sends. |
| Duplicate trigger event | Idempotency key prevents a second send. |
| SMTP provider down | Retry with backoff; on exhaustion, dead-letter + alert Control Room; never lose the intent (outbox/inbox). |
| Missing contact email | Log + dead-letter the trigger; surface to Control Room rather than failing silently. |
| RabbitMQ down | Can't receive triggers, but the direct bus-down alert path still works; queued triggers process on recovery. |
