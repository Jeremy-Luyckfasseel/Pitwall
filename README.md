# Pitwall

**An event-driven, loosely-coupled platform that integrates a karting track's systems** —
check-in, lap timing, the bar, billing, the back office, the public website, and the
trackside leaderboard — into one coherent whole.

> Status: **design complete, implementation pending.** This repo currently holds the full
> architecture analysis and the message contract. Code lands service-by-service from here.

A **portfolio / learning project**, built solo, that showcases production-grade
craftsmanship of a polyglot microservice platform. The karting track is a deliberately
representative scenario chosen to exercise the architecture.

---

## What makes it interesting

- **Loosely coupled.** No service calls another. The **only** thing shared between services
  is the message contract in [`/contract`](contract/) — each owns its own language,
  framework, and database.
- **Event-driven.** Every meaningful action publishes an event; others react. A thing that
  happens in one service is automatically known by every service that cares.
- **Fault-tolerant.** Each service keeps working from its **own local copy** of the data it
  needs (event-carried state transfer) even when its peers — or RabbitMQ itself — are down.
- **"No computer says no."** Every failure path resolves to a defined, graceful outcome.
  Each service carries a sad-path table proving it.
- **Bus-only health.** No HTTP `/health` endpoints — liveness is 1-second heartbeats plus
  the Control Room's self-ping, which also distinguishes a downed service from a downed bus.

## Architecture at a glance

```
                     ┌──────────────────────────────────────┐
                     │               RabbitMQ                │
                     │   (one durable topic exchange/service) │
                     └──────────────────────────────────────┘
         publishes ▲            ▲     ▲     ▲             ▲ consumes
                   │            │     │     │             │
   Timing · Identity · Driver · CRM · Booking · Frontend · Billing ·
   Mailing · Leaderboard · Bar/POS          + Control Room (monitoring)
```

| Service | System of record |
|---|---|
| **Timing** | scans, lap times, PR detection, the simulator |
| **Identity** | the one canonical `masterId` (master UUID) per person (email-deduped) |
| **Driver** | racing profile, full lap history, canonical PR |
| **CRM** | person/company, contacts, consent, loyalty |
| **Booking** | session/heat schedule + capacity |
| **Frontend** | public site + admin UI + credentials/auth |
| **Billing** | tabs, charges, invoices/receipts, gapless numbering |
| **Mailing** | outbound email (reacts only) |
| **Leaderboard** | live trackside standings (read model) |
| **Bar/POS** | bar orders (incl. anonymous sales) |
| **Control Room** | monitoring, alerting, and privacy-saga coordination |

Communication is **RabbitMQ-only**. The single sanctioned non-bus call is Control Room →
Mailing for the "bus is down" alert (which the bus itself can't carry).

## The contract

[`/contract`](contract/) is the **only** coupling point between the polyglot services. Every
message is JSON wrapped in a common envelope, with its payload validated against a versioned
JSON Schema. Producers validate on publish, consumers validate on receipt; invalid messages
are logged and dead-lettered, never silently dropped.

## Repository layout

```
docs/
  analysis/        architecture overview, message bus, engineering standards,
                   service blueprint, per-service designs, and the Q&A decision log
  adr/             Architecture Decision Records (0001–0010)
  pitwall_brief.md the original brief (historical context)
contract/
  schemas/         the envelope + per-event JSON Schemas
  examples/        a worked example per event
services/          the polyglot services (each: own Dockerfile, DB, tests)
tests/
  contract/        contract-layer validation (pytest against /contract)
  conformance/     the cross-language conformance harness + e2e smoke (4th gate)
CLAUDE.md          operating guide for working in this repo
```

## Tests — four CI-gated layers (NFR23)

Per `docs/analysis/03-engineering-standards.md`, every change passes four layers before merge
(no merge on red):

1. **Unit** — pure logic, no I/O (per service).
2. **Integration** — real RabbitMQ + DB via testcontainers (per service).
3. **Contract** — every published/consumed message validated against `/contract`
   (`make contract-test`).
4. **e2e smoke** — the **cross-language conformance harness** drives the real service binaries
   against one real RabbitMQ and asserts identical observable bus behavior
   (`make smoke`; see [`tests/conformance/`](tests/conformance/)). This is the required merge
   gate; a flaky scenario goes to a non-blocking **quarantine lane**, never `@skip`.

## Deploy — per-service tag → GHCR → VPS pull (ADR-0007)

Local Compose is dev/staging; the **VPS hosts production only**. Deploy is **tag-driven, not
branch-driven** — merging to `main` never deploys. Pushing a **per-service tag** `‹svc›-vX.Y.Z`
(e.g. `timing-v0.1.0`) builds **only that service's** image and pushes it to **public GHCR**
([`.github/workflows/release.yml`](.github/workflows/release.yml)); a **pull-based poller** on the
VPS picks up the new image and recreates **only that container** (no server build, no inbound SSH).
Rollback = redeploy the previous immutable GHCR image. Full operator runbook + the shared-VPS
guardrails: [`deploy/README.md`](deploy/README.md).

## Project status & roadmap

- ✅ **Analysis & design** — architecture, ADRs, per-service specs, and the message contract.
- ✅ **PRD** — capabilities, requirements, NFRs, data governance (decisions back-recorded
  into `docs/analysis` and `/contract`).
- ⬜ **Implementation** — build order: message bus + Control Room first, then Timing (with
  its simulator) as the first end-to-end slice, then the rest. See the brief's build order.

## Read more

- **Architecture overview** — [`docs/analysis/01-architecture-overview.md`](docs/analysis/01-architecture-overview.md)
- **Message bus, envelope & event catalog** — [`docs/analysis/02-message-bus-and-contracts.md`](docs/analysis/02-message-bus-and-contracts.md)
- **Engineering standards & CI/CD** — [`docs/analysis/03-engineering-standards.md`](docs/analysis/03-engineering-standards.md)
- **Service blueprint** (the baseline every service implements) — [`docs/analysis/04-service-blueprint.md`](docs/analysis/04-service-blueprint.md)
- **Every decision + its rationale** — [`docs/analysis/00-questions-and-answers.md`](docs/analysis/00-questions-and-answers.md)
- **Decision records** — [`docs/adr/`](docs/adr/)

---

> **The golden rule of this repo:** never assume. Every design choice was made by asking a
> question and recording the answer. If something isn't decided in the Q&A log, it's an open
> question — ask, don't guess. See [`CLAUDE.md`](CLAUDE.md).
