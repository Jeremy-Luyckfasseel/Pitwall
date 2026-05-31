# Service: Frontend

> The public-facing website and the **owner of end-user credentials/auth**. Decisions:
> [Q&A](../00-questions-and-answers.md) Rounds 3 & 8. Conforms to the
> [service blueprint](../04-service-blueprint.md).

## Purpose
Let drivers register, log in, browse and book sessions, and view their personal lap
history and PRs — all while staying decoupled (works even if peers are down).

## System of record (owns)
- **Credentials / auth**: email + password hash (or OAuth linkage), sessions/tokens.
  **No other service ever sees credentials.** A **separate admin credential set**
  (config-seeded, no public signup) gates the admin UI — distinct from driver auth.
- A **local read-model** built from events for everything it displays (session
  schedule, driver history, PRs). Reads hit local data only.

## Scope
- Registration + login (owns credentials).
- Session browsing.
- Booking with confirmation flow.
- Personal lap history + PR display.
- **Profile editing** — publishes `profile.edit_requested` (Driver applies racing fields,
  CRM applies contact/consent).
- **Privacy self-service** — publishes `privacy.erasure_requested` / `privacy.export_requested`
  for the logged-in driver (ADR-0009).
- **Admin UI** ([ADR-0010](../../adr/0010-admin-operator-control-plane.md)) — gated by a
  **separate admin login** (distinct from driver credentials; admin accounts **seeded from
  config/env**, no public signup). Lets the operator manage the schedule
  (`schedule.change_requested` → Booking) and start/end sessions
  (`session.control_requested` → Timing).

## Identity interplay
Frontend never mints user ids. On registration it stores credentials locally, then
publishes `identity.lookup_requested {email}`; on `identity.resolved {userId}` it links
the credential record to the canonical `userId`. "Already registered?" is detected from
Frontend's **own** credential store.

## Events
**Publishes** (`frontend.events`): `user.registration_submitted`, `booking.requested`,
`profile.edit_requested`, `schedule.change_requested` (admin), `session.control_requested`
(admin), `privacy.erasure_requested`, `privacy.export_requested`.
**Consumes**: `identity.resolved`, `booking.confirmed`/`booking.rejected`,
`session.scheduled`/`session.rescheduled`/`session.cancelled`,
`driver.history_appended`/`driver.pr_updated`, `crm.person_updated` — all to maintain
the local read-model.

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Register with an already-used email | Frontend's own store detects it → "please log in" path; no duplicate account. |
| Booking rejected (full) | Show the `alternatives[]` from `booking.rejected` and let the user pick one. |
| Driver/Booking service down | Pages still render from the local read-model (possibly slightly stale). |
| RabbitMQ down | Read-only browsing/history still works (local read-model); write intents (book/register) are queued via outbox and confirmed when the bus returns; UI shows "pending". |
| Stale read-model after restart | Rebuild by replaying events from the last marker. |
