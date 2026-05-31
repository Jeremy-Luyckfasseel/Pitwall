# CLAUDE.md — Operating guide for Pitwall

This file is loaded into context every session. Read it first. It is the contract for
*how to work in this repo*, not a full spec — the full spec is in `docs/analysis/`.

---

## 0. THE GOLDEN RULE: never assume

**Do not make assumptions about anything.** Pitwall's design was produced by asking the
user a question for every decision and recording the answer. If something you need is:

- **answered** in [`docs/analysis/00-questions-and-answers.md`](docs/analysis/00-questions-and-answers.md)
  → follow it exactly;
- **not answered anywhere** → it is an **open question**. **Stop and ask the user.** Do
  not guess, do not pick a "reasonable default", do not infer from other projects. After
  they answer, record the Q&A in `00-questions-and-answers.md` before building.

If you ever catch yourself about to write "I'll assume…", that's the signal to ask
instead. The whole project exists to demonstrate deliberate, documented decisions.

---

## 1. What Pitwall is

An event-driven, loosely-coupled platform that integrates a karting track's systems
(check-in, lap timing, bar, billing, website, leaderboard, CRM). A **portfolio /
learning** project, built **solo**, where each service is treated as its own "team".
Services are **polyglot** — each may use its own language/framework/database. The
**only** thing shared between them is the message contract in [`/contract`](contract/).

Full picture: [`docs/analysis/01-architecture-overview.md`](docs/analysis/01-architecture-overview.md).

## 2. Non-negotiable rules

1. **No APIs between services. Communication is RabbitMQ-only.** No HTTP between
   services, no synchronous RPC as a primary pattern. The single, documented exception
   is Control Room → Mailing for the **bus-down alert** only
   ([ADR-0008](docs/adr/0008-single-bus-down-api-exception.md)).
2. **No HTTP `/health` endpoints.** Liveness is bus-only: 1 s heartbeats + the Control
   Room's self-ping ([ADR-0004](docs/adr/0004-bus-only-health-and-self-ping.md)).
3. **Event-carried state transfer.** Each service keeps its **own local copy** of the
   data it needs and must keep working when peers *or RabbitMQ* are down
   ([ADR-0002](docs/adr/0002-event-carried-state-transfer.md)).
4. **One canonical `userId`** issued solely by Identity; every service joins on it and
   never mints its own ([ADR-0003](docs/adr/0003-identity-as-uuid-issuer.md)).
5. **Validate every message** (in and out) against `/contract` JSON Schemas. Invalid →
   log + dead-letter, never silently drop.
6. **No "computer says no".** Every failure path has a defined graceful outcome — see
   each service's sad-path table.
7. **Every service conforms to the [service blueprint](docs/analysis/04-service-blueprint.md).**
8. **Data governance is first-class.** Every service consumes `privacy.erasure_requested`
   and **deletes/anonymizes** its slice (a tombstone blocks resurrection), honors retention
   (7 y financial · 2 y operational · 90 d raw logs), and minimizes PII
   ([ADR-0009](docs/adr/0009-data-governance.md)).

## 3. The system (10 services + Control Room)

`Timing` · `Identity` · `Driver` · `CRM` · `Booking` · `Frontend` · `Billing` ·
`Mailing` · `Leaderboard` · `Bar/POS` · + `Control Room`.

The **operator/admin** acts through a Frontend admin UI (separate, config-seeded login) —
schedule management and **operator-started sessions** — plus the Control Room dashboard
([ADR-0010](docs/adr/0010-admin-operator-control-plane.md)). The Control Room also
coordinates the privacy saga (erasure tracking + export assembly). No 11th service.

Per-service designs: [`docs/analysis/services/`](docs/analysis/services/).
Event catalog & envelope: [`docs/analysis/02-message-bus-and-contracts.md`](docs/analysis/02-message-bus-and-contracts.md).

## 4. Engineering standards (summary)

Full version: [`docs/analysis/03-engineering-standards.md`](docs/analysis/03-engineering-standards.md).

- **Tests (CI-gated):** unit + integration (real RabbitMQ + DB) + contract (validate
  against `/contract`) + an e2e smoke. No merge on red.
- **Style:** per-language linter + formatter, pre-commit hooks, CI gate, Conventional
  Commits.
- **Reliability:** outbox + idempotent inbox + DLQ + event-store replay
  ([ADR-0005](docs/adr/0005-outbox-inbox-event-store.md)).
- **Observability:** structured JSON logs + a `correlationId` propagated across the bus.
- **Repo:** monorepo; `services/<name>/` each self-contained with its own Dockerfile.
- **Deploy:** branches = envs (dev local, **VPS = prod only**); **per-service tags**
  `‹svc›-vX.Y.Z` build → GHCR → VPS pulls only the changed container
  ([ADR-0007](docs/adr/0007-monorepo-per-service-deploy.md)).
- **Config:** `.env` locally and on the VPS; `.env.example` committed; real `.env`
  gitignored.

## 5. Definition of Done

A change is done only when it: conforms to the blueprint; passes all four test layers;
validates against `/contract` (with schemas+examples committed for new events); is
linted with a Conventional Commit; handles its sad paths; logs with a correlation id
and leaks no secrets; updates the relevant docs; and — if it changes architecture —
adds an ADR.

## 6. Where everything lives

| Need | Path |
|---|---|
| Every decision + its rationale (Q&A log) | `docs/analysis/00-questions-and-answers.md` |
| Architecture overview | `docs/analysis/01-architecture-overview.md` |
| Bus topology, envelope, event catalog | `docs/analysis/02-message-bus-and-contracts.md` |
| Engineering standards & CI/CD | `docs/analysis/03-engineering-standards.md` |
| Mandatory service baseline | `docs/analysis/04-service-blueprint.md` |
| Per-service designs | `docs/analysis/services/` |
| Decision records | `docs/adr/` |
| The shared message contract | `contract/` |
| Original brief | `docs/pitwall_brief.md` |

> Note: this design **deliberately overrides the brief** in two places (bus-only health
> instead of HTTP `/health`; the Control Room sits on the bus). Both are recorded and
> justified — see [ADR-0004](docs/adr/0004-bus-only-health-and-self-ping.md). The brief
> is historical context; `docs/analysis/` is the source of truth.
>
> PRD-phase decisions (admin/operator control plane, data governance & privacy,
> anonymous POS sales + VIES invoicing, operator-started sessions) are recorded in Q&A
> **Rounds 13–16** and [ADR-0009](docs/adr/0009-data-governance.md)/[ADR-0010](docs/adr/0010-admin-operator-control-plane.md).
