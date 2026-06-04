# Pitwall — Architecture Overview

> Authoritative source for *why the system is shaped this way*. Every statement here
> traces back to a recorded decision in
> [`00-questions-and-answers.md`](./00-questions-and-answers.md). Nothing in this
> document may contradict that log. If you need something that isn't decided there,
> **do not assume — ask** (see [`/CLAUDE.md`](../../CLAUDE.md)).

## 1. What Pitwall is

Pitwall integrates the disconnected operational systems of a karting track —
check-in, lap timing, the bar, billing, the back office, the public website, and the
trackside leaderboard — into one event-driven whole. A thing that happens in one
service (a driver checks in, a lap is set, a session ends) is automatically known by
every other service that cares, with **no direct calls between services**.

This is a **portfolio / learning project** built by a **single developer** where each
service is treated as its own "team". The goal is to showcase production-grade
craftsmanship of an event-driven, loosely-coupled, polyglot microservice platform.

## 2. Core principles (from the brief, made concrete)

| Principle | How Pitwall realises it |
|---|---|
| **Loosely coupled** | No service calls another. The only coupling is the shared message **contract** (`/contract`). Each service owns its own database. |
| **Event-driven** | Every meaningful action publishes an event; others react. |
| **Fault tolerant** | A service keeps serving from its **own local copy** of the data it needs even when peers — or RabbitMQ itself — are down. Outbound events queue in an **outbox** and flush on recovery. |
| **No "computer says no"** | Every service doc carries a **sad-path table**: each failure scenario maps to a defined graceful outcome. |

## 3. The one hard rule: no APIs between services

Services communicate **exclusively through RabbitMQ**. There are **no HTTP APIs
between services** and **no synchronous RPC** as a primary pattern. This is stronger
than the brief and is deliberate.

There is exactly **one sanctioned exception**, narrowly scoped and documented: when
**RabbitMQ itself is down**, the Control Room makes a **direct call to Mailing** to
send the "bus is down" alert (the bus can't carry it). This exception exists for that
single purpose and is forbidden anywhere else. See
[ADR-0008](../adr/0008-single-bus-down-api-exception.md).

## 4. Inter-service data sharing: Event-Carried State Transfer (ECST)

Because a service must keep working when the bus or a peer is down, **no service
queries another at request time**. Instead:

- Each service that produces data publishes the **full relevant state** in its events.
- Each consumer keeps its **own local copy** (read-model) of just the slice it needs,
  keyed on the canonical `masterId`.
- Reads always hit the local copy — fast, and available even during an outage.
- Writes use the **outbox pattern**: the state change and the outgoing event are
  persisted in the same local transaction, then a relay publishes to RabbitMQ and
  retries until the bus is reachable.
- Consumers use an **idempotent inbox**: every message carries an id; reprocessing a
  duplicate is a no-op.

Consequence: the system is **eventually consistent**, which is acceptable and
expected. See [ADR-0002](../adr/0002-event-carried-state-transfer.md).

## 5. Identity & the canonical `masterId`

A single **Identity** service is the *sole issuer* of a canonical `masterId` (UUID) —
exactly **one per person**. Every other service stores that `masterId` as its join key
and never mints its own per-user id. This guarantees all services are talking about
the same person.

- Identity stores only `masterId` + a natural key (email) + status + timestamps.
  **No passwords, no rich profile, no fuzzy merging.**
- A service that needs a `masterId` publishes `identity.lookup_requested {requestId,
  email}`; Identity returns `identity.resolved {requestId, email, masterId}` — minting a
  new UUID if the email is unknown, reusing the existing one if not. The new-vs-
  returning decision lives entirely inside Identity; consumers idempotently upsert.
- The `masterId` is **embedded directly in the QR code**, so Timing reads it at the gate
  with no lookup.

See [ADR-0003](../adr/0003-identity-as-uuid-issuer.md).

## 6. Service roster (10 services + Control Room)

```
                         ┌─────────────────────────────────────────────┐
                         │                 RabbitMQ                      │
                         │   (one durable topic exchange per service)    │
                         └─────────────────────────────────────────────┘
   publishes ▲                       ▲   ▲   ▲                     ▲ consumes
             │                       │   │   │                     │
  ┌──────────┴─────┐  ┌────────┐ ┌───┴─┐ │ ┌─┴────┐  ┌─────────┐  ┌┴──────────┐
  │ Timing (+sim)  │  │Identity│ │Driver│ │ │ CRM  │  │ Booking │  │ Frontend  │
  └────────────────┘  └────────┘ └─────┘ │ └──────┘  └─────────┘  └───────────┘
  ┌────────────────┐  ┌────────┐ ┌───────┴──┐  ┌──────────┐    ┌───────────────┐
  │ Billing        │  │Mailing │ │Leaderboard│  │ Bar/POS  │    │ Control Room  │
  └────────────────┘  └────────┘ └──────────┘  └──────────┘    └───────────────┘
```

| # | Service | Owns (system of record) | Doc |
|---|---------|-------------------------|-----|
| 1 | **Timing** | scans, lap times, PR detection, transponder→masterId map, simulator | [timing](./services/timing.md) |
| 2 | **Identity** | canonical `masterId`, email dedupe | [identity](./services/identity.md) |
| 3 | **Driver** | racing profile + full lap history + canonical PR | [driver](./services/driver.md) |
| 4 | **CRM** | person/company, contacts, consent, loyalty | [crm](./services/crm.md) |
| 5 | **Booking** | session/heat schedule + capacity | [booking](./services/booking.md) |
| 6 | **Frontend** | public UI + credentials/auth + local read-model | [frontend](./services/frontend.md) |
| 7 | **Billing** | tabs, charges, invoices/receipts, invoice numbers | [billing](./services/billing.md) |
| 8 | **Mailing** | outbound email (reacts only) | [mailing](./services/mailing.md) |
| 9 | **Leaderboard** | live standings (read model) | [leaderboard](./services/leaderboard.md) |
| 10 | **Bar/POS** | bar orders | [bar-pos](./services/bar-pos.md) |
| — | **Control Room** | monitoring/alert read-model | [control-room](./services/control-room.md) |

## 7. Health & monitoring (bus-only)

There are **no HTTP `/health` endpoints** (a deliberate override of the brief). The
Control Room knows the state of the world purely through RabbitMQ:

1. Every service **publishes a heartbeat every 1 second** to the Control Room.
2. The Control Room **publishes a self-ping through RabbitMQ every 1 second** and
   listens for it. If the self-ping doesn't return → **RabbitMQ is down**.
3. **Disambiguation:**
   - self-ping returns **and** a service's heartbeat is missing → *that service* is down;
   - self-ping missing → *the bus* is down.
4. A service is marked **DOWN** after **3 consecutive missed heartbeats (~3 s)**, which
   fires an alert.

Docker's own `healthcheck:` uses a **bus-connectivity script / liveness touch-file**
(updated by the heartbeat loop), not an HTTP probe. See
[ADR-0004](../adr/0004-bus-only-health-and-self-ping.md).

## 8. Environments, build & deploy

| Concern | Decision |
|---|---|
| **Orchestration** | Docker Compose now; Kubernetes documented as a later stretch goal. |
| **Dev/staging** | Runs on the **local machine** (full stack via Compose). |
| **Production** | A single **VPS (7 GB RAM / 75 GB disk, shared with another app)** — production only. |
| **Branches** | **GitHub Flow** (Round 21): `story/<epic>.<story>` → PR (squash) → `main`; **no long-lived `dev` branch** (solo). `main` = always-green integration + release line; a per-service tag promotes to prod (deploy ≠ merge). |
| **Releases** | **Per-service tags** (e.g. `timing-v1.2.0`) trigger CI to rebuild/redeploy **only that service** — independent versioning in a monorepo. |
| **Image delivery** | CI builds → pushes to **GHCR** → the VPS **pulls** and recreates only changed containers. |
| **Persistence** | **Database-per-service**, each service's own engine choice; footprint right-sized at deploy time. |
| **Config/secrets** | `.env` locally and on the VPS (same values, user-accepted caveat); `.env.example` committed; real `.env` gitignored; CI holds only deploy creds. |
| **Observability** | Structured JSON logs + a correlation/trace id propagated across the bus. No heavy central log/metrics stack. |

## 9. Where to go next

- The contract & event catalog: [`02-message-bus-and-contracts.md`](./02-message-bus-and-contracts.md)
- Engineering rules & CI/CD: [`03-engineering-standards.md`](./03-engineering-standards.md)
- The mandatory baseline every service implements: [`04-service-blueprint.md`](./04-service-blueprint.md)
- Per-service designs: [`services/`](./services/)
- Decision rationale: [`../adr/`](../adr/)
