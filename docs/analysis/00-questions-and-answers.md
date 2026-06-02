# Pitwall — Analysis Phase: Questions & Answers Log

> This is the authoritative record of every clarifying question asked during the
> analysis phase and the answer given. **No decision in any other doc is allowed
> to contradict this file.** If something is not answered here, it is still an
> open question and must NOT be assumed — see [Open Questions](#open-questions).
>
> Format: each round groups related questions. `Q` = question asked, `A` = answer
> given, `→` = the resulting decision / interpretation recorded into the design.

Date started: 2026-05-31
Project: Pitwall — integrated event-driven platform for karting tracks
Source brief: `docs/pitwall_brief.md`

---

## Round 0 — Foundations (project context & working style)

**Q0.1 — What is the real-world context and goal of Pitwall?**
A: **Portfolio / learning project.** Built to learn microservices + RabbitMQ and to
show off. Quality matters but there are no real end users yet.
→ Docs and code rules aim for *production-grade craftsmanship as a showcase*, but we
do not need real-world compliance/scale hardening (e.g. PCI, GDPR audits, HA clusters)
unless explicitly chosen later.
> **AMENDED 2026-06-01 (PRD phase — see [Round 16](#round-16--data-governance--privacy-prd-phase)):**
> Data governance / privacy **has now been explicitly chosen** as in-scope (consent,
> retention, erasure, audit, minimization). PCI and HA clusters remain out of scope.

**Q0.2 — What does "team" mean for repo/doc organization?**
A: **Solo developer; "team" is just the label for each service.** One author builds
everything.
→ Docs are organized one file per service ("team"), but written for a single author.
A service can expand from a single `.md` to a folder if it needs multiple docs.

**Q0.3 — How is the tech stack decided across services?**
A: **Per-service freedom (polyglot).** Each service may pick its own language/
framework/database. Only the RabbitMQ contract is shared. This showcases true loose
coupling.
→ The shared **message contract** is therefore the single most important artifact,
since it is the *only* coupling point between services.

**Q0.4 — How do we work through the ~60+ detailed questions?**
A: **Interactive themed rounds.** Ask batches of questions per theme; record each
question and answer as we go.
→ This document is that record.

---

## Round 1 — The Message Bus (RabbitMQ)

**Q1.1 — Exchange topology?**
A: **One topic exchange per owning service.** (e.g. `timing.events`, `driver.events`).
→ Each service owns and declares its own durable topic exchange. Publishers publish
only to their own exchange; consumers bind queues to the exchanges they care about.
Routing keys follow `<entity>.<action>` within an owner's exchange (e.g.
`timing.events` exchange, routing key `lap.recorded`). Strong ownership boundary.

**Q1.2 — Message format & contract storage?**
A: **JSON + JSON Schema, kept in a `contract/` folder in this repo** (not a separate
repo).
→ Payloads are JSON, validated against versioned JSON Schema files in `/contract`.
Every service validates incoming and outgoing messages against these schemas. JSON
chosen over XML/XSD for first-class polyglot tooling support.

**Q1.3 — Inter-service data sharing, given "each team must keep functioning even if
peers OR RabbitMQ itself are down"?**
A: **Event-carried state transfer + outbox/inbox pattern.** No synchronous
cross-service calls (no HTTP APIs, no RPC-over-bus as a primary pattern).
→ Each service keeps its **own local copy** of the data it needs, kept fresh by
events. Publishing uses the **outbox pattern** (event written to local store in the
same transaction as the state change, then relayed to RabbitMQ; flushed when the bus
recovers). Consumers use an **idempotent inbox** to dedupe. When the bus is down a
service keeps serving local reads/writes and queues outbound events.

**Q1.4 — Failure handling / no lost events?**
A: **Durable queues + manual ack + DLQ + event store.** Persistent messages, manual
acknowledgement after successful processing, dead-letter queues for poison messages,
retry with backoff, AND every event persisted to an event store/log so a restarting
service can replay history to catch up.

**Q1.5 — Service breakdown: add a CRM service?**
A: **Yes — add CRM and split it from Driver.**
→ Final service list is **8 services**:
- **Driver** = racing identity: who the racer is, transponder/QR mapping, lap history,
  personal records. Source of truth for *performance* data.
- **CRM** = commercial relationship: person vs company, contact details, marketing/
  communication consent, loyalty, company↔employee links. Source of truth for
  *customer* data. (Billing and Mailing lean on this.)
- Plus the brief's Timing, Frontend, Booking, Billing, Mailing, Leaderboard.
- Registration/profile editing in **Frontend** is pure UI that publishes *intents*;
  Driver/CRM remain the source of truth.

---

## Round 2 — Cross-cutting infrastructure

**Q2.1 — Orchestration?**
A: **Docker Compose now, Kubernetes later.**
→ A single `docker-compose.yml` (per environment) brings up all 8 services +
RabbitMQ + control room + databases. A path to k8s is documented as a stretch goal,
not built now.

**Q2.2 — Heartbeat / health model?**
A: **Everything over RabbitMQ — NO HTTP `/health` endpoints at all.** Three layers:
1. Each service **publishes a heartbeat over RabbitMQ every 1s** to the control room.
2. The control room **publishes a self-ping through RabbitMQ every 1s** and listens
   for it; if it does not come back → **RabbitMQ is down**.
3. Disambiguation: self-ping returns but a service's heartbeat is missing → **that
   service is down**; self-ping missing → **the bus is down**.
→ **DELIBERATE OVERRIDE OF THE BRIEF.** The brief mandates a `/health` endpoint and
says the control room checks health "directly, not on the bus." We are intentionally
replacing that with bus-only heartbeats. The brief's *goal* (detect a silent service)
is still met via the self-ping disambiguation. Trade-off accepted: no out-of-band
check, so a service whose process is alive but whose bus client is wedged looks
"down" (acceptable). Consequence: Docker's `healthcheck:` uses a **bus-connectivity
script or liveness touch-file**, not an HTTP probe.

**Q2.3 — CI/CD & deploy granularity?**
A: **Per-service tags drive deploys, in a monorepo.**
→ Tags like `timing-v1.2.0` trigger a workflow that rebuilds/redeploys **only that
service**. Confirmed monorepo-with-folders supports this (tag prefix parsing). Each
"team" versions and releases independently.

**Q2.4 — Branch → environment mapping?**
A: **Branches = environments, tags = production releases.** BUT dev env will NOT run
on the VPS (resource limits) — see Q2.6.
→ `dev` branch = integration; `prod`/`main` = release line. CI tests + builds images
on branch pushes. A per-service `v*` tag is what promotes to **production on the
VPS**. "Deployed" (env) vs "released" (tagged) are separated.

**Q2.5 — Image build & delivery?**
A: **Build in CI → push to GitHub Container Registry (GHCR) → VPS pulls.**
→ CI builds each service image and pushes to GHCR. On release the VPS pulls new
images and `docker compose up -d` recreates only changed containers. No build tooling
on the server.

**Q2.6 — Dev/staging environment location (VPS is 7 GB RAM / 75 GB disk, shared with
another app)?**
A: **Local machine = dev/staging (Docker Compose); VPS = production only.**
→ Full stack runs locally for dev/integration. Only production lands on the VPS,
avoiding a doubled footprint that the VPS can't hold.

**Q2.7 — Persistence: database-per-service?**
A: **Yes, database-per-service — each service's own choice of engine.** Footprint
**right-sized later** (no constraint imposed now; no resource-budget doc yet).
→ No shared database; each service owns its datastore. A production memory/resource
budget pass is deferred until deploy time.

---

## Round 3 — Cross-cutting: config, observability, security, schema evolution

**Q3.1 — Config & secrets management?**
A: **A `.env` locally and a `.env` on the VPS — identical values in both.**
→ Env-var / 12-factor config. `.env.example` with placeholders is committed as
documentation; real `.env` files are gitignored. CI needs only deploy credentials
(GHCR + SSH), not app secrets. **Caveat recorded (user-accepted):** dev and prod use
the *same* secret values; for a real launch these should be split so a leaked dev
secret can't reach prod. Easy to change later.

**Q3.2 — Observability?**
A: **Structured logs + correlation IDs.**
→ Every service emits structured (JSON) logs. Every event carries a correlation/
trace ID propagated across the bus, so one driver's journey can be followed across
all services. The control room surfaces aggregate stats. No heavy central
log/metrics infra for now.

**Q3.3 — End-user authentication?**
A: **Frontend owns auth/credentials; a dedicated Identity service issues the
canonical user id.** (See Q3.4.)
→ Frontend handles registration/login and stores password hashes / OAuth — credentials
never leave Frontend. Internal services trust the bus.

**Q3.4 — Add a dedicated Identity service?**
A: **Yes — 9th service.**
→ **Identity** is the *sole issuer* of the canonical master `userId` (a UUID every
other service stores as its join key; no service mints its own per-user id). It also
performs **identity resolution / merging** — e.g. linking an anonymous
transponder/QR scan (created by Timing before any account exists) to a later
registered account. It publishes events such as `user.registered` and
`identity.merged`. **Identity never sees passwords.** This directly powers the
brief's edge case *"conflicting driver data arrives from two services."*

**Q3.5 — Schema evolution policy?**
A: **Versioned event types + tolerant readers.**
→ Each event carries a `schemaVersion`. New fields are additive/optional; consumers
ignore unknown fields (tolerant reader). A breaking change gets a new routing key /
version and both run side by side during migration. Policy documented in `/contract`.

---

## Service roster (consolidated)

After Rounds 1 & 3 the platform is **9 services + 1 control room**:

| # | Service | Source of truth for | Notes |
|---|---------|--------------------|-------|
| 1 | **Timing** | scans, lap times, PRs | primary data source; includes simulator |
| 2 | **Identity** | canonical `userId`, identity resolution | NEW; never handles passwords |
| 3 | **Driver** | racing identity, lap history per driver | keyed on `userId` |
| 4 | **CRM** | person/company, contacts, consent, loyalty | NEW; commercial relationship |
| 5 | **Booking** | session/heat schedule, capacity | |
| 6 | **Frontend** | end-user UI + credentials/auth | publishes intents only |
| 7 | **Billing** | tabs, charges, invoices/receipts | |
| 8 | **Mailing** | outbound email | reacts only, never self-initiates |
| 9 | **Leaderboard** | live standings (read model) | |
| — | **Control Room** | monitoring/alerting dashboard | bus heartbeats + self-ping |

---

## Analysis status

**All planned question rounds are COMPLETE** (Rounds 0–12 below). Every decision the
design docs rely on is recorded in this file. If a future question arises that is not
answered here, it is a genuine open question — **do not assume**; ask the user and add
it to this log (see [[CLAUDE.md]] no-assumptions rule).

Remaining work after analysis is *implementation*, not more discovery, unless new
requirements surface.

---

## Round 4 — Control Room (monitoring & alerting)

**Q4.1 — How does the control room get dashboard stats (active drivers, sessions
today, laps recorded)?**
A: **Subscribe to domain events and build its own read model.**
→ The control room consumes domain events (`lap.recorded`, `session.started`, driver
events, …) *and* heartbeats, maintaining its own stats database. No querying of other
services. (Note: this means the control room IS a bus participant — a further
deliberate departure from the brief's "does not sit on the bus", consistent with the
Q2.2 heartbeat-over-bus decision.)

**Q4.2 — Alert delivery?**
A: **Dashboard + email, with a single direct-API fallback for the bus-down case.**
→ Normally: an alert appears on the dashboard immediately AND the control room
publishes an alert event that Mailing turns into an email. **Exception:** when
RabbitMQ itself is down, the bus can't carry the alert, so the control room makes a
**direct API call to Mailing** to send the "RabbitMQ is down" alert. This is the
**only sanctioned cross-service API in the entire system** and is forbidden for any
other purpose — it exists solely for this edge case.

**Q4.3 — Down-detection threshold?**
A: **3 consecutive missed 1s heartbeats (~3s) → mark DOWN and fire alert.**
→ Tolerates one or two transient blips; detects real outages within ~3s.

**Q4.4 — Control-room implementation/tech?**
A: **Build a custom lightweight web dashboard with its own DB — no ELK/Kibana.**
→ Decided after weighing Kibana: ELK was rejected because Elasticsearch is RAM-heavy
(~1.5–3 GB) on the constrained VPS and its real-time alerting is a poor fit for the
"3 missed heartbeats → alert now" logic. A small custom component (bus consumer +
status/stats/alert-history DB + live web UI via websockets/SSE) is both *simpler* for
this job and a better portfolio showcase of the event-driven monitoring design
(including the self-ping bus-down detection).

---

## Round 5 — Identity service (scope realigned with the user)

> During the Timing round it emerged that the assistant and user had different mental
> models of Identity. The assistant's initial "identity resolution / fuzzy merging"
> framing was **dropped** in favour of the user's simpler, correct design.

**Q5.1 — Final Identity scope?**
A: **A minimal canonical-UUID issuer that also stores a natural key (email) to
de-duplicate people.** No passwords, no rich profile, no fuzzy merging.
→ Identity stores: `userId` (canonical UUID) + natural key (email; optionally
name/phone later) + status + timestamps. It guarantees **one UUID per person**.

**Q5.2 — How do other services obtain a UUID / know new-vs-returning?**
A: **Event-driven lookup, email as the key; reply is just the UUID (no `isNew`).**
→ A service publishes `identity.lookup_requested {requestId, email}`. Identity looks
up the email: if known, returns the existing UUID; if not, mints a new one. It replies
with `identity.resolved {requestId, email, userId}`. **No `isNew` flag** — the
new-vs-returning logic lives *entirely inside Identity*; consumers don't care because
they idempotently upsert on the UUID. No synchronous API — fully bus-based.

**Q5.3 — Where does the canonical UUID live for scanning?**
A: **Embedded directly in the QR code.** Timing reads the UUID straight from the QR;
no lookup needed at the gate for QR users.

**Q5.4 — Consequence for the "conflicting driver data" edge case?**
A: Because Identity no longer does fuzzy resolution/merging, that brief edge case is
**re-homed** — to be solved by field-level versioning / defined source-of-truth
precedence in the edge-case round. (Open.)

---

## Round 6 — Timing service

**Q6.1 — Two scan points?**
A: **Yes — entry-gate check-in + start-finish lap are distinct.**
→ Entry-gate scan = check-in/identification (opens billing tab, marks driver present):
event `driver.checked_in`. Start-finish scan = lap crossing: event `lap.recorded`.

**Q6.2 — Scan-to-identity?**
A: QR codes carry the master UUID directly. **Timing keeps its own local `users`
table** (copy keyed by UUID via ECST) to validate scans and attach UUIDs to laps
without calling anyone.

**Q6.3 — Transponder→userId mapping owner?**
A: **Timing owns it.** As the hardware-facing service, Timing stores
transponder(hardware-id)→userId in its own DB, assigned when a transponder is handed
out (operator/check-in). QR (UUID-embedded) needs no mapping; transponders do.

**Q6.4 — Walk-in driver (no account/QR)?**
A: **Counter/kiosk registers them on the spot → Identity mints UUID → QR/transponder
issued immediately.** Everyone is a real user with a UUID from first contact; no
anonymous state, no merging.

**Q6.5 — Session start/end authority?**
A: **Booking owns the planned schedule; Timing emits the ACTUAL `session.started`/
`session.ended`** (physical reality / operator action / first cars on track). Booking
reconciles its schedule to reality (enables late-running cascade).

**Q6.6 — Personal records compute vs store?**
A: **Timing detects PR-broken; Driver is system-of-record.**
→ Timing keeps a local copy of each driver's all-time PR (seeded by Driver events via
ECST), compares each lap, and publishes `personal_record.broken`. Driver stores the
authoritative lap history + canonical PR and re-publishes it.

**Q6.7 — Lap validity rules?**
A: **Configurable minimum-lap-time filter + first crossing = start marker.**
→ A configurable min lap time rejects double-reads/bounce (e.g. crossings <10s
apart ignored). First start-finish crossing starts the clock (out-lap, not counted);
each subsequent valid crossing = one lap.

**Q6.8 — Simulator?**
A: **Fully configurable** — N drivers, randomized lap-time distribution, session
length; toggled via env/flag. Can drive a full end-to-end demo without hardware.

**Q6.9 — Scanner offline mid-session?**
A: **Persist-first + gap detection + operator alert.**
→ Every lap is written to Timing's durable local store (and outbox) the instant it's
read, so prior laps are never lost if the scanner/bus then drops. Timing detects the
scanner going silent, flags a gap, and publishes `scanner.offline` for the control
room. Crossings physically missed during the outage are unrecoverable but
acknowledged — never faked.

---

## Round 7 — Driver & CRM services

**Q7.1 — Driver system-of-record content?**
A: **Full lap-by-lap history + per-session summaries + canonical all-time PR**, keyed
on `userId`. Driver consumes `lap.recorded` and `session.ended`. The complete racing
record.

**Q7.2 — Profile attribute split (Driver vs CRM)?**
A: **Driver = racing profile; CRM = person/contact.**
→ Driver holds racing-specific attributes (racing number, preferred kart class,
leaderboard nickname, racing stats). CRM holds the human/commercial side (legal name,
email, phone, address, company link, consent, loyalty). Clean bounded contexts, both
keyed on the same `userId`.

**Q7.3 — CRM company / B2B model?**
A: **Companies + employee links + company billing.**
→ CRM models companies, links persons as employees, and marks who/what is billed to a
company — this powers Billing's company-invoice path. Real B2B karting scenario.

**Q7.4 — CRM consent & loyalty scope?**
A: **Marketing consent flags + simple loyalty.**
→ CRM stores communication/marketing consent (Mailing must check it before sending
non-transactional email) and a simple loyalty notion (points or tier).

---

## Round 8 — Booking & Frontend services

**Q8.1 — Late-session reschedule cascade?**
A: **Auto-cascade with a configurable changeover buffer, published as events.**
→ When Timing reports a session ran past its slot, Booking recalculates downstream
start times, applies a buffer, and publishes `session.rescheduled` events; Mailing
notifies affected bookers. Fully automatic — no "computer says no".

**Q8.2 — Booking flow & "session full" edge case?**
A: **Booking owns capacity and confirms/rejects via events.**
→ Frontend publishes `booking.requested`; Booking is the single authority on
capacity, atomically reserves a spot or rejects, publishing `booking.confirmed` or
`booking.rejected {reason, alternatives}`. A full session returns the next available
alternatives — never a dead end.

**Q8.3 — Frontend scope?**
A: **Full public site:** account registration/login (owns credentials), session
browsing, booking with confirmation, personal lap history + PR display.

**Q8.4 — Frontend read strategy (stay decoupled)?**
A: **Local read-model built from events.**
→ Frontend subscribes to relevant events (driver history, PRs, session schedule) and
maintains its own local read-model/cache, so pages render from local data even when
Driver/Booking are offline. Pure ECST.

---

## Round 9 — Billing & Mailing services

**Q9.1 — Billing tab lifecycle?**
A: **Open on check-in, accrue session + bar, close on session end.**
→ A tab opens on `driver.checked_in`, accrues the session charge plus bar orders, and
closes when the session ends → produces a receipt/invoice.

**Q9.2 — Private person requests a formal invoice (edge case)?**
A: **Always allow; capture invoice details on demand.**
→ Default for individuals is a receipt, but anyone can request a formal invoice;
Billing captures the needed fields (name/address/VAT optional) and issues a proper
invoice even with no company link. No dead end.

**Q9.3 — Invoice documents & numbering?**
A: **Billing owns a gapless sequential invoice-number sequence and generates a PDF.**
→ Billing renders a PDF receipt/invoice and publishes `invoice.issued` (with document
reference) for Mailing to deliver.

**Q9.4 — Mailing behavior?**
A: **Event-triggered, consent-aware, idempotent, templated.**
→ Mailing reacts only to events (booking confirmed, session summary, PR alert,
invoice issued). Transactional mail always sends; marketing mail checks CRM consent.
Idempotent (no duplicate sends on redelivery), templated, logs every send. Real SMTP
in prod, captured locally in dev (e.g. Mailhog).

---

## Round 10 — Leaderboard & remaining edge cases

**Q10.1 — Leaderboard behavior?**
A: **Best-lap order; reset on session start; tie = earliest set; show status.**
→ Live standings ordered by best lap; updates instantly on `lap.recorded`; resets on
`session.started`; ties broken by whoever set the time first; shows session status
(active/finished); uses the leaderboard nickname from Driver.

**Q10.2 — Service restart / catch-up (all services)?**
A: **Event-store replay + idempotent inbox + last-processed marker.**
→ Each service records the last event it processed; on restart it replays from the
durable event store/queue past that marker; the idempotent inbox dedupes reprocessing.
Read-model services (Leaderboard, Frontend) can fully rebuild from replay.

**Q10.3 — Conflicting driver/user data (re-homed edge case)?**
A: **Source-of-truth precedence + field-level last-write-wins.**
→ Each field has a defined owning service (contact→CRM, racing number→Driver,
laps→Timing); the owner's writes win. For same-owner races, last-write-wins by event
timestamp + version. Conflicts are logged. Deterministic, no merge guesswork.

**Q10.4 — Document sad paths?**
A: **Yes — a sad-path table per service doc.**
→ Each service doc gets a table of failure scenarios → defined graceful outcome,
satisfying the brief's "no computer says no" principle.

### Edge-case coverage summary (brief's 6 + extras)
| Brief edge case | Resolved in | Mechanism |
|---|---|---|
| Session runs late → downstream shifts | Q8.1 | Auto-cascade + buffer, `session.rescheduled` |
| Private person requests formal invoice | Q9.2 | Always allow, capture details on demand |
| Service restarts mid-session, catches up | Q10.2 | Event-store replay + inbox + marker |
| Conflicting driver data from two services | Q10.3 | SoT precedence + field-level LWW |
| Scanner offline mid-session, no lost laps | Q6.9 | Persist-first + gap detect + alert |
| Booking a full session | Q8.2 | Reject with alternatives, never a dead end |
| RabbitMQ itself down | Q2.2/Q4.2 | Self-ping detect + direct CR→Mailing alert |
| Peer service down | Q1.3 | ECST local copies + outbox flush on recovery |

---

## Round 11 — Code-quality rules & engineering standards

**Q11.1 — Testing strategy (every service)?**
A: **Unit + integration + contract + e2e smoke.**
→ Unit tests for logic; integration tests against a real RabbitMQ + DB
(testcontainers); **contract tests** validating every published/consumed message
against the JSON Schemas in `/contract`; an end-to-end smoke test of the happy path.
CI gates on all of them.

**Q11.2 — Style/quality enforcement (polyglot)?**
A: **Per-language linter + formatter, pre-commit hooks, CI gate, Conventional
Commits.**
→ Each service uses its language's standard linter+formatter, enforced by pre-commit
hooks AND a CI gate. Conventional Commits drive history + automated per-service
versioning/changelogs.

**Q11.3 — ADRs?**
A: **Yes — ADRs seeded from this Q&A** in `docs/adr/`, short numbered records for the
big decisions (one-exchange-per-service, ECST+outbox, Identity-as-UUID-issuer,
bus-only health, custom control room, …). Preserves the *why*.

**Q11.4 — Service blueprint?**
A: **Yes — a mandatory service blueprint/checklist** every service conforms to
regardless of language: outbox+inbox, idempotent consumers, schema validation in/out,
1s heartbeat, structured logging + correlation IDs, graceful shutdown, Dockerfile +
healthcheck script, `/contract` conformance, sad-path handling.

---

## Round 12 — Final gap (bar orders) + deliverables

**Q12.1 — Where do bar orders come from? (Brief mentions a bar but lists no bar
service.)**
A: **A thin Bar/POS service publishes `bar.order_placed`.**
→ A small **10th service** (bar terminal/POS) publishes `bar.order_placed {userId,
items, total}`; Billing consumes and adds to the tab. Includes a simulator like
Timing. The bar is just another event producer.

**Q12.2 — Confirm documentation deliverables?**
A: **Full set:** `01-architecture-overview`, `02-message-bus-and-contracts`
(topology + envelope + full event catalog), `03-engineering-standards`,
`04-service-blueprint`, one doc per service + control room under `services/`,
`docs/adr/` with seeded ADRs, a `/contract` folder skeleton (envelope + example
schema), and `CLAUDE.md` at the repo root enforcing the no-assumptions rule.

---

## Final service roster (10 services + control room)

| # | Service | Source of truth for | Key events (illustrative) |
|---|---------|--------------------|---------------------------|
| 1 | **Timing** | scans, lap times, PR detection | `driver.checked_in`, `lap.recorded`, `session.started/ended`, `personal_record.broken`, `scanner.offline` |
| 2 | **Identity** | canonical `userId` (one per person), email dedupe | `identity.lookup_requested`(in), `identity.resolved`(out) |
| 3 | **Driver** | racing profile + full lap history + canonical PR | `driver.profile_updated`, `driver.pr_updated` |
| 4 | **CRM** | person/company, contacts, consent, loyalty | `crm.person_updated`, `crm.company_updated`, `crm.consent_changed` |
| 5 | **Booking** | session/heat schedule + capacity | `booking.confirmed/rejected`, `session.rescheduled` |
| 6 | **Frontend** | end-user UI + credentials/auth | publishes `*.requested` intents; local read-model |
| 7 | **Billing** | tabs, charges, invoices/receipts, invoice numbers | `tab.opened`, `invoice.issued` |
| 8 | **Mailing** | outbound email (reacts only) | consumes; emits `email.sent` for audit |
| 9 | **Leaderboard** | live standings (read model) | consumes `lap.recorded`, `session.*` |
| 10 | **Bar/POS** | bar orders | `bar.order_placed` |
| — | **Control Room** | monitoring/alert read-model | consumes heartbeats + domain events; self-pings bus |

---

# PRD-phase rounds (2026-06-01)

> Rounds 13–16 record decisions taken while producing the **platform PRD**
> (`_bmad-output/planning-artifacts/prds/prd-Pitwall-2026-05-31/`). They **extend and in
> places amend** the analysis-phase rounds above. Still **10 services + Control Room** — no
> new service. Companion contract schemas live in `/contract`; companion ADRs are
> [0009](../adr/0009-data-governance.md) and [0010](../adr/0010-admin-operator-control-plane.md).

## Round 13 — Admin role, admin UI & scheduling (PRD phase)

**Q13.1 — Who creates and manages the session schedule? (Was unspecified.)**
A: **A default recurring daily schedule** (same session times/capacities each day) is the
baseline; an **operator/admin** can override it (adjust times/capacity, cancel) via a
**Frontend admin UI**.
→ Booking **seeds a recurring daily template** and remains the single authority on capacity
and the reschedule cascade. The admin UI publishes a **`schedule.change_requested`** intent
to `frontend.events`; Booking binds and applies it, emitting the corresponding
`session.scheduled` / `session.rescheduled` / `session.cancelled`. Adds an **admin role**
(the operator persona gains a second surface alongside the Control Room).

**Q13.2 — How is admin access modelled?**
A: **A separate admin account/login**, isolated from public driver credentials.
→ Frontend owns a **separate admin credential set** (distinct from driver creds). Admin
accounts are **seeded from config/env at deploy** (no public admin signup); password reset
is admin-initiated / config-based (**OQ-6 resolved**). The admin UI is gated by this admin
auth.

## Round 14 — Operator-started sessions (PRD phase)

**Q14.1 — How does a live session start/end? (Refines [Q6.5](#round-6--timing-service).)**
A: In **live operation** the operator **explicitly starts** a session (race-control
green-light); it ends when its **planned duration elapses or the operator ends it**. The
simulator generates these boundaries automatically.
→ The admin/operator publishes a **`session.control_requested {sessionId, action:
start|end}`** intent to `frontend.events`; **Timing** binds it and turns it into the ACTUAL
`session.started` / `session.ended`. **Sad paths:** start-already-started = idempotent
no-op; end-never-started = graceful reject (logged); operator-end vs auto-end-on-duration
race = first applies, second is a no-op. This makes "operator action" the **canonical** live
trigger in Q6.5.

## Round 15 — POS invoicing, anonymous sales & VAT (PRD phase)

**Q15.1 — Where are invoices requested, and for what? (Refines [Q9.2](#round-9--billing--mailing-services).)**
A: **At the POS**, for **any** purchase (food, drinks, a session, the whole visit). The
default document is a **receipt**; a formal **invoice** is issued on request.
→ For a driver on a tab, an invoice can be requested for the visit. A **receipt is
anonymous** (no buyer details); a **formal invoice legally requires name + address** (VAT
**only if the buyer is a business**).

**Q15.2 — Anonymous purchases (food/drinks with no account)?**
A: **Yes** — anonymous POS sales with **no `userId`**, paid immediately, no tab.
→ **`bar.order_placed` gets an optional `userId`** (absence = anonymous sale); **stays
schemaVersion 1** (additive, tolerant-reader-safe). Billing records an anonymous sale as an
**immediately-settled** charge with no tab, **no PII**, gapless document number. An
anonymous buyer who wants an invoice must **declare it at the POS before paying** and supply
the legally-required details — **no retroactive invoice** (no linkage key).

**Q15.3 — VAT-number validation?**
A: **Best-effort via the EU VIES service.**
→ When a business buyer supplies a VAT number, the POS **may validate + enrich** name/address
via **VIES** (an external third-party service — **not** a Pitwall service, so no breach of
the bus-only rule). Best-effort: if VIES is unreachable, fall back to manual entry and still
issue the invoice. Never on the critical path.

**Q15.4 — Re-issue / double-document?**
A: **Credit/void + a new document**, never a silent duplicate.
→ If a document was already issued (e.g. a receipt) and a different one is later required,
Billing issues a **credit/void + a new document**, preserving the gapless ledger.

## Round 16 — Data Governance & Privacy (PRD phase)

> **Amends [Q0.1](#round-0--foundations-project-context--working-style):** GDPR-style data
> lifecycle is now explicitly **in scope**.

**Q16.1 — Is privacy/data-lifecycle in scope?**
A: **Yes — a first-class Data Governance & Privacy concern.**
→ Consent, retention, erasure, audit, and minimization are now in scope. PCI and HA clusters
remain out. See [ADR-0009](../adr/0009-data-governance.md).

**Q16.2 — Retention windows? (OQ-2 resolved.)**
A: **Financial/invoices = 7 years** (Belgian accounting-law requirement); **operational/
racing data = while the account is active + 2 years** after last activity, then purged;
**raw scan/transponder logs = 90 days**. All configurable.

**Q16.3 — Erasure (right-to-be-forgotten) orchestration?**
A: **Choreographed — no new service.**
→ Frontend publishes **`privacy.erasure_requested {userId}`**; **every service**
self-erases/anonymizes its local slice and emits **`privacy.erased {userId, service,
mode}`** on its own exchange; the **Control Room** (the existing aggregator) tracks
completion across services on its dashboard. An erased `userId` is recorded as a
**tombstone** so a replayed/late event cannot resurrect it. Erasure of a user with an **open
tab/active session defers** until close.

**Q16.4 — What does "anonymize" mean per service? (OQ-5 resolved.)**
A: Under retention, **Billing keeps** the invoice number + amounts + VAT + date but **nulls
name/address/email/userId**. **Every other service fully deletes** its local slice for that
`userId` (Driver history, CRM contact, Leaderboard nickname, Timing transponder map,
Frontend credentials).

**Q16.5 — Data export / portability?**
A: A driver can request an export of their personal data; it is **assembled from the owning
services and delivered**.
→ Frontend publishes **`privacy.export_requested {userId}`**; each owning service publishes
its slice (**`privacy.data_provided {userId, service, payload}`**); the **Control Room**
(privacy aggregator) assembles the bundle and emits **`privacy.export_ready {userId,
documentRef}`**, which **Mailing** delivers. *(Gives the Control Room a lightweight
privacy-coordination role alongside monitoring — recorded as a deliberate reuse, not a new
service.)*

**Q16.6 — Audit, consent & minimization?**
A: Privacy-relevant actions (consent change, erasure request/completion, export) are recorded
in an **append-only audit trail** carrying the `correlationId` **and the acting identity**
(driver or admin actor). Marketing consent defaults to **no**; transactional mail always
sends (consistent with Q7.4/Q9.4). **Data minimization** (each service stores only the slice
it needs — already ECST) is now an explicit governance requirement.

## Round 17 — Online stored value: wallets, gift cards & the payments edge (PRD phase, 2026-06-02)

> **Amends [Q15/§8 OQ-4](#round-15--pos-invoicing-anonymous-sales--vat-prd-phase):** online
> payment was a non-goal; it is now reopened in a **single, narrow funnel**. Originated from
> the UX run (gift cards/vouchers added to the site IA, flagged as a scope extension). Still
> **10 services + Control Room** — no new service. Companion ADR:
> [0011](../adr/0011-external-payments-edge.md). Companion contract schemas in `/contract`.

**Q17.1 — Gift cards/vouchers and online payment are now wanted. How far does online payment open?**
A: Confined to **loading stored value**. The user expanded "gift cards" into a **prepaid
wallet**: load money online and **spend it on everything** (food, sessions, …) at any POS and
online.
→ The **only** online card operation is **topping up a wallet or buying a gift card**. All
*spending* stays a **bus-side balance debit** or a POS terminal transaction — **never a fresh
online card charge**. §8/OQ-4 is **narrowed, not broken**: spending still happens at the
touchpoint, exactly as before.

**Q17.2 — How does a synchronous PSP fit a bus-only system?**
A: The bus-only rule governs comms **between Pitwall services**, not Pitwall↔outside-world.
Pitwall already crosses external edges synchronously (Frontend⇄browsers, POS→VIES, Mailing→
SMTP); a **PSP is a fourth external edge**.
→ A thin **payments edge inside Frontend** is the sole speaker of the PSP's HTTPS. The PSP
**webhook is external inbound** (like a browser POST or a VIES reply), **not** inter-service
RPC, so **bus-only is preserved, not excepted** (≠ the ADR-0008 carve-out). PSP = **Mollie**
(v1, behind a swappable port); card data never touches Pitwall (**PCI SAQ-A**).
See [ADR-0011](../adr/0011-external-payments-edge.md).

**Q17.3 — Who owns vouchers + online payment — Billing, or a new component?**
A: **Frontend** hosts the payments edge; **Billing** owns the **stored-value ledger + VAT**
(where gapless numbering and invoice logic already live). No 11th service.
→ The edge emits **`payment.captured`** (frontend.events); Billing, as ledger system-of-record,
credits the balance and emits the balance facts (**`wallet.topped_up`**, **`giftcard.issued`**,
**`wallet.debited`**, **`giftcard.redeemed`**) to `billing.events`. The capture→event step is
**outbox-buffered**, so a confirmed payment is never lost when the bus is down.

**Q17.4 — One instrument or two? Redemption model?**
A: **Two instruments, one ledger primitive.** An account **wallet** (keyed on `userId`) and a
transferable **bearer gift card** (a redeemable code, no PII). A gift card is, in effect, an
anonymous/transferable wallet.
→ Stored value is a **balance** (not single-use): **partial spend** supported; spendable at
**POS + online**; a gift-card code can be **loaded into a wallet or spent at the POS**.
Redemption is **idempotent** (inbox + ledger guard) — no double-spend.

**Q17.5 — VAT treatment of stored value?**
A: **Multi-purpose voucher (MPV).** Stored value buys supplies at potentially different VAT
rates (track time, bar), so the rate is not fixed at load.
→ **Loading value is non-taxable** (a payment-on-account, no VAT at sale); **VAT is accounted
at spend/redemption** through Billing's existing document logic (FR61–65, FR81–82) — reusing
the existing VAT machinery rather than a parallel engine. A single-purpose voucher (VAT at
sale) was rejected: it would lock the balance to one supply/rate.

**Q17.6 — Refunds, cancellation & expiry?**
A: **Non-refundable** once the PSP capture settles; **cancellation only before capture**.
→ Balances carry a **configurable expiry** with an **EU-compliant minimum validity**; expiry
is **logged, never silently dropped**. A failed refund/expiry action dead-letters + alerts
Control Room. Stored value falls under the **7-year financial retention** window (DG-1); a
**wallet balance defers erasure** like an open tab (FR78); a **bearer gift card carries no PII**.

**Q17.7 — E-money / PSD2 regulation?**
A: **Out of scope** for this portfolio build.
→ Spendable stored value can trigger EU e-money/PSD2 duties (KYC/AML, safeguarding) above
thresholds. Pitwall treats stored value as a **closed-loop facility under a configurable
balance cap**, no KYC/AML — a documented limitation, not a production-compliant wallet.
