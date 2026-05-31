# Pitwall — Analysis Phase

The complete, decision-backed analysis for Pitwall. Every design choice here traces to
a recorded question and answer — **nothing is assumed**. If you hit something not
covered, ask the user and add it to the Q&A log (see [`/CLAUDE.md`](../../CLAUDE.md)).

## Read in this order
1. [`00-questions-and-answers.md`](./00-questions-and-answers.md) — the authoritative
   Q&A log. Source of truth for *what was decided and why*.
2. [`01-architecture-overview.md`](./01-architecture-overview.md) — the big picture.
3. [`02-message-bus-and-contracts.md`](./02-message-bus-and-contracts.md) — RabbitMQ
   topology, the message envelope, and the full event catalog.
4. [`03-engineering-standards.md`](./03-engineering-standards.md) — code quality,
   testing, CI/CD, deployment.
5. [`04-service-blueprint.md`](./04-service-blueprint.md) — the baseline every service
   must implement.
6. [`services/`](./services/) — one design doc per service (10 + Control Room).
7. [`../adr/`](../adr/) — architecture decision records (the rationale).

## The system at a glance
10 services + Control Room, polyglot, RabbitMQ-only (no inter-service APIs),
event-carried state transfer, bus-only health. Built solo as a portfolio project;
production runs on a single VPS, dev runs locally.

| Service | Owns |
|---|---|
| [Timing](./services/timing.md) | scans, lap times, PR detection, simulator |
| [Identity](./services/identity.md) | canonical `userId` (one per person) |
| [Driver](./services/driver.md) | racing profile + lap history + canonical PR |
| [CRM](./services/crm.md) | person/company, contacts, consent, loyalty |
| [Booking](./services/booking.md) | session schedule + capacity |
| [Frontend](./services/frontend.md) | public UI + credentials/auth |
| [Billing](./services/billing.md) | tabs, charges, invoices/receipts |
| [Mailing](./services/mailing.md) | outbound email (reacts only) |
| [Leaderboard](./services/leaderboard.md) | live standings (read model) |
| [Bar/POS](./services/bar-pos.md) | bar orders |
| [Control Room](./services/control-room.md) | monitoring + alerting |
