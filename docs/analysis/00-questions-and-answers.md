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
> **⚠ Branch model superseded by [Round 21](#round-21--branching-model-github-flow-no-long-lived-dev-branch-2026-06-04):** the `dev`-*branch*-as-integration clause no longer holds — the build is solo, so it's **GitHub Flow** (`story/* → PR → main`, no long-lived `dev` branch). The *environment* split (local = dev/staging, VPS = prod) and the deploy-by-tag semantics here are **unchanged**.

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

## Round 18 — Control Room: live message-flow view + AI remediation agent (PRD phase, 2026-06-02)

> **Origin:** the UX run for **Surface 4 — the Control Room dashboard** (decisions D8 + D12 in
> the UX `.decision-log.md`). Two items extend the Control Room beyond Round 4's design and were
> flagged — not assumed — for an explicit decision, exactly as the gift-cards UX flag was in
> Round 17. **Still 10 services + Control Room** — no new *bus* service (see Q18.3). Companion ADR:
> [0012](../adr/0012-control-room-observability-and-ai-agent.md).

**Q18.1 — The Control Room gains a live "Message Flow" view (a service-to-service event-flow
diagram). How does it get the data — the Control Room doesn't observe arbitrary traffic today?**
A: A **metadata-only bus observation tap**, in scope for the Control Room build.
→ The Control Room adds a passive observer (a wildcard/firehose-style bind, or the broker's
firehose tracer) that records only **routing metadata — `{from-service, event-type, to/consumers,
timestamp}` — never message bodies/payloads**. That is enough to animate the graph + per-edge
counts. **No PII** crosses the tap (payloads are never read); the stream is **sampleable** under
load and **not persisted** beyond the rolling live window. The diagram is **logical** (publisher→
consumer) with the honest caption that everything physically routes via RabbitMQ (ADR-0001).
**Full-payload observation is explicitly rejected** (PII/volume/retention) — see §8.

**Q18.2 — Jeremy also wants an AI agent that, on an alert, diagnoses the fault and opens a fix PR
(or writes up likely causes). Is it in scope?**
A: **Deferred — a decided, documented post-MVP capability.** It is recorded now (this round +
ADR-0012 + a PRD post-MVP requirement) but **not built in the current MVP**; the core 10 services
+ Control Room build is unchanged. (Jeremy: "an extra, do it at the end.")
→ Only its **Control Room footprint** is designed in the UX: a single **read-only one-line hint**
per alert (a short diagnosis + a "proposed PR #NN" deep-link **out** to the agent's own page). The
agent itself ships later.

**Q18.3 — Does the AI agent break "10 services + Control Room — no 11th service"?**
A: **No.** It is modeled as an **external ops/dev tool**, **outside** the Pitwall service count —
**like CI/CD or the Timing/Bar simulators**, not a first-class bus service.
→ It **observes** alerts at an allowed edge and **acts on GitHub** (open PR / write issue) — a
**fifth external edge**, the same category as Frontend⇄browsers, POS→VIES, Mailing→SMTP, and the
PSP payments edge (Round 17). So the non-negotiable holds verbatim; **no 11th bus service**, and
no inter-service API. (An 11th first-class service, and folding it into the read-only Control Room,
were both rejected — the former breaks the rule, the latter the Control Room's observe-only posture.)

**Q18.4 — Guardrails for an AI that can change the codebase?**
A: **Propose, never dispose.** The agent **never auto-merges to production**.
→ It may only **open a pull request (or an issue)** for **human review + merge**; it runs with a
**least-privilege, scoped GitHub token** (open PRs/issues only — no merge, no force-push, no deploy);
its scope is **read-diagnose-suggest**. No autonomous change to running infrastructure. A documented
safety boundary, not an autonomous remediation system.

**Q18.5 — What does the agent consume to diagnose?**
A: At minimum the **`alert.raised`** signal; optionally read-only context — **logs / the event
store / the Round-18 message-flow metadata** — to form a diagnosis.
→ Consumption is **read-only and diagnostic**; the agent is a *consumer/observer plus a GitHub-edge
actor*, never a publisher of Pitwall domain events. Exact inputs are an ADR-0012 / build-time detail.

## Round 19 — Architecture phase: master UUID, register-first, polyglot, AI assistant (2026-06-03)

> Decisions taken during the **`bmad-create-architecture` workflow** (output:
> `_bmad-output/planning-artifacts/architecture.md`), pressure-tested in multi-agent review. They
> **extend and in places correct** earlier rounds. Per the golden rule they are recorded here before
> building. Still **10 services + Control Room** (the AI assistant is an external ops tool, not a bus
> service). Companion ADR: **[0013](../adr/0013-admin-ai-assistant.md)**; companion `/contract` rename
> applied.

**Q19.1 — What is the canonical person identifier called?**
A: The **master UUID**, surfaced on the wire as the field **`masterId`** (renamed from `userId`).
Identity remains its sole issuer (one per person; ADR-0003).
→ `userId` → **`masterId`** renamed across **`/contract`** (envelope unaffected — it lives in `data`)
and the **live spec docs** (`docs/analysis/01–04`, all service docs, `CLAUDE.md`, the PRD). The wire
field is `masterId` (UUID v4, lowercase-hyphenated); envelope `id` is UUID v7. **History preserved:**
Rounds 0–18 keep their original `userId` wording for the same concept (this round is the rename of
record). ADR-0003's terminology to be refreshed to "master UUID".

**Q19.2 — Walk-in identity: register-first, or a raw-token buffer? (Resolves reconcile finding B1.)**
A: **Register-first is canonical** — exactly as [Q6.4](#round-6--timing-service) already decided.
There is **no anonymous racing identity**.
→ Identity is resolved **at check-in, before the driver goes on track** (counter/kiosk captures email
→ `identity.lookup_requested` → on `identity.resolved` the `masterId` is bound to the QR/transponder
that is then issued). A lap is **never emitted for an unresolved token**. An **unknown token at the
start-finish line** is an **operator-surfaced exception** (held + flagged, never minted, never an
anonymous lap, never dropped) — not a normal path. The earlier **raw-token-buffer-as-normal-path**
reading in **FR39/FR51** and in `timing.md`/`bar-pos.md` sad-paths **contradicted Q6.4 and is
corrected** (those texts updated this round). Amelia's seam-bug risk (orphaned laps with no join key)
is thereby designed out.

**Q19.3 — Anonymous sales vs the one-master-UUID rule. (Resolves reconcile finding B2.)**
A: Absence of a `masterId` is permitted **only** for an **immediate-pay bar food/drink sale that
issues no invoice** (the anonymous carve-out, FR49/FR81). **Any session and any formal invoice
REQUIRES the `masterId`.**
→ NFR15 ("every service joins on the canonical id") holds everywhere it matters; the lone exception is
a walk-up POS line item that never enters the session or billing join. This is the precise rule that
defuses the FR81-vs-NFR15 tension.

**Q19.4 — Per-service languages (the polyglot showcase, kept solo-maintainable)?**
A: **Three languages, organized as tiers** (Amelia's 2–3 cap; Go leans on the 7 GB RAM ceiling):
**Go** (Timing, Identity, Leaderboard, Control Room — real-time/infra, low idle RAM), **Python**
(Driver, CRM, Billing, Mailing — records/documents), **TypeScript** (Frontend, Booking, Bar/POS —
experience/edge). Versions verified June 2026 (Go 1.26.4, Python 3.14.x, Node 24 LTS, Next.js 16.2.x).
→ **Database-per-service stays real and read-write** (not a shared engine): **SQLite-by-default**
(embedded, ~zero idle RAM), **PostgreSQL only for Billing** (financial integrity, gapless numbering,
stored-value ledger). RAM (not disk, not time) is the binding constraint; disk is unconstrained.

**Q19.5 — DLQ behaviour?**
A: **TTL-based auto-retry** (a `<consumer>.<purpose>.retry` queue dead-lettering back to source) **plus
a delivery-count cap → a terminal `<consumer>.<purpose>.parking` quarantine queue** + Control Room
alert. TTL alone would loop poison messages forever; the cap terminates them.

**Q19.6 — How is the Round-18 Message-Flow tap actually fed? (Pins ADR-0012's open mechanism.)**
A: A **passive wildcard-bound observer queue** that reads **only envelope routing metadata**
(`type`/`source`/inferred consumers/`occurredAt`) and **never the `data` payload** — chosen over the
broker firehose tracer (which captures full messages → PII/volume risk). Sampleable, not persisted
beyond the rolling window. No PII crosses the tap; no new contract event.

**Q19.7 — The admin AI assistant: scope, safety, and how it stays bus-only?**
A: An **admin chatbot** that is an **external ops tool** (outside the 10-services-+-Control-Room count —
like CI or the simulators), optimized to be **always as correct as possible**, and **distinct from the
Round-18 GitHub remediation bot** (opposite safety posture).
→ **Reads (MVP):** the LLM never computes/invents figures — it maps natural language onto a **fixed,
typed, versioned set of query functions** over a **dedicated CQRS reporting read-model** (a bus
consumer built via the same ECST/idempotent-inbox machinery; carries a `last-synced` watermark and
flags lag/bus-down rather than faking live — C1). A single **read-only MCP edge** (AI ↔ read-model);
**never MCP-to-every-service** (live fan-out is less correct, slower, and breaks read-path fault
tolerance). **Writes (phase two):** publish the **existing `frontend.events` admin intents** (same
path as the admin UI — no new write path, no bus bypass); **destructive ops require an explicit human
confirm**; every action audited (`correlationId` + acting identity = AI + invoking admin; DG-4).
Recorded as **[ADR-0013](../adr/0013-admin-ai-assistant.md)**.

**Q19.8 — MVP scope?**
A: The MVP is the **full platform** — all 10 services + Control Room, with the **complete bus-only
health model and all heartbeats in scope**. This is a **no-deadline** solo learning/portfolio build
("all the time in the world"). Walking-skeleton-first (Identity → Timing → Leaderboard + Control Room
heartbeats, surviving a mid-session bus kill) is the **build order**, not a scope limit. Post-MVP: AI
writes (phase two) + the GitHub remediation bot (FR94); live Mollie may be stubbed.

**Q19.9 — Anything from the architecture phase still to propagate?**
A: Yes, tracked here so the corpus cannot silently drift:
→ (a) **A7 (reconcile):** Q0.1's "no GDPR audits" deferral is **already superseded** by the
[Round 16](#round-16--data-governance--privacy-prd-phase) amendment noted on Q0.1 — reaffirmed, no
further action. (b) **Wire-hardening** decided in `architecture.md` (camelCase wire, RFC3339-`Z`
millisecond timestamps, integer-cents money, enum value-sets, big-int-as-string, `envelopeVersion`,
relaxing `additionalProperties:false` for additive evolution, masterId regex) is to be applied to
`/contract` **incrementally** (the `masterId` rename landed this round). (c) **Durability:** a CI
**grep-gate** should fail the build on `userId` / raw-token-buffer / register-first contradictions so
the corpus stays coherent. (d) Add the **Wire Contract Rules digest** to `CLAUDE.md` + `CONTRIBUTING.md`.

## Round 20 — Epics & stories phase: MVP boundary of the Admin AI Assistant (2026-06-04)

> Decision taken during the **`bmad-create-epics-and-stories` workflow** (output:
> `_bmad-output/planning-artifacts/epics.md`). Recorded here per the golden rule because it **resolves an
> ambiguity** the architecture left open. Companion: **[ADR-0013](../adr/0013-admin-ai-assistant.md)** §4
> corrected to match.

**Q20.1 — Is the Admin AI Assistant's read-only analytics in the MVP, or post-MVP?**
A: **Post-MVP.** The **entire** `tools/admin-ai-assistant/` — read-only analytics (reporting CQRS
read-model + read-only MCP edge + NL→query-function mapping) **and** writes-via-intents (phase two) —
is deferred to the **post-MVP shelf**, alongside the Round-18 GitHub remediation bot (FR94). The MVP is
exactly the **10 services + Control Room** (Q19.8). This resolves the architecture's internal ambiguity
— `architecture.md` called read analytics "(read-only MVP)" in the assistant's own phasing while filing
the whole tool under "Future (post-MVP)"; the assistant carries **no FR number**, confirming it sits
outside the MVP FR set. The Control Room's one-line **AI hint footprint** (UX-DR22) is still designed in
the MVP (Epic 12); only the assistant tool itself ships later.
→ **ADR-0013 §4 updated** ("Read analytics = MVP" → "post-MVP shelf"); epics.md post-MVP shelf cites
this round.

## Round 21 — Branching model: GitHub Flow, no long-lived `dev` branch (2026-06-04)

> Decided while setting up the build phase. **Simplifies the branch half of [Q2.4](#round-2--deploy-cicd--persistence) /
> [ADR-0007](../adr/0007-monorepo-per-service-deploy.md)** for a solo developer. The *environment* and
> *release* decisions (Q2.3, Q2.5, Q2.6 — local Compose = dev/staging, VPS = prod, per-service tags
> promote to prod) are **unchanged**.

**Q21.1 — Do we keep a long-lived `dev` integration branch (story → dev → main), or go story → main?**
A: **Story → main (GitHub Flow). No long-lived `dev` branch.** Short-lived
`story/<epic>.<story>-slug` branches → PR → **squash** → `main`. `main` is the always-green
integration + release line.
→ Rationale: the build is **solo**, so there are no concurrent integrations to buffer — story branches
already isolate WIP and CI gates each PR. Crucially, **deploys are tag-driven, not branch-driven**
(Q2.3 / ADR-0007): merging to `main` never touches prod; only a per-service tag `‹svc›-vX.Y.Z` promotes
to the VPS. So a `dev` buffer protects nothing — `main` can be the single integration target and stay
releasable. This **supersedes the "`dev` branch = integration" clause of Q2.4**; the "`dev` = *local
environment*" mapping (Q2.6) stands — that was always about where code runs, not a git branch. Each
story commit carries a `Story: <epic>.<story>` trailer (see `CONTRIBUTING.md`).

## Round 22 — Front-of-house POS / counter, and the user-ownership model (2026-06-04)

> Decided in the **`bmad-correct-course` workflow** (output:
> `_bmad-output/planning-artifacts/sprint-change-proposal-2026-06-04.md`), triggered by reviewing the
> system map (`docs/diagrams/pitwall-map.html`). The build had not started (epics & stories just done).
> Primary item = a scope expansion (companion **[ADR-0014](../adr/0014-front-of-house-pos-counter.md)**);
> secondary item = a clarification of the existing user model (no redesign). Still **10 services +
> Control Room**.

**Q22.1 — Should Bar/POS be just the bar, or the front-of-house counter?**
A: **The front-of-house POS / counter.** Beyond bar sales it also (a) **registers walk-ins** on-site,
(b) **sells & books track time** (live availability + `booking.requested`), and (c) **takes session
payment incl. prepay**. The "POS" was previously the bar only; the counter experience (register + book +
pay a session in one on-site interaction) was unmodeled. Booking stays the **single capacity authority**
(FR23), Identity stays the **sole id-issuer** (FR1/ADR-0003), register-first (FR39) holds, and it's all
**bus-only** (no inter-service API). New FRs **FR95–FR98**; persona **S2** broadened to "counter/bar
staff". Naming unchanged (`bar-pos` / `bar.events`).

**Q22.2 — How is session prepayment modeled on the bus?**
A: **Reuse existing Billing primitives — no new money event.** A prepaid session is an
**immediately-settled tab charge** (or `wallet.debited` for balance pay), accounted by the same
gapless-numbering / MPV-VAT machinery as any other charge (FR59–61, FR87). Prepay is **in addition to**
the existing postpaid tab-at-session-end path. Insufficient wallet → partial split / card-cash fallback
(FR87). *(Rejected: a new `booking.paid`/`pos.session_paid` event — unnecessary given the reuse.)*

**Q22.3 — Who publishes the on-site registration intent for a walk-in?**
A: **The POS owns counter registration.** Bar/POS captures the email and publishes
`identity.lookup_requested` itself, consuming `identity.resolved` to issue the QR/transponder — so the
POS is genuinely the counter. *(Rejected: delegating to the Timing/kiosk check-in flow.)* It still never
mints a `masterId`; Identity resolves it (register-first, FR39).

**Q22.4 — "Every service should be able to CRUD a user" — what does the model actually allow?**
A: **Each service fully CRUDs the *slice of the user it owns*; that is the model, and it is already
satisfied.** Clarifying the ownership split (no redesign — preserves [ADR-0002](../adr/0002-event-carried-state-transfer.md)
ECST + [ADR-0003](../adr/0003-identity-as-uuid-issuer.md) sole-issuer):
→ **Identity** is only the **thin UUID issuer** (`masterId` + email) — *not* the user's rich profile.
→ **CRM is THE rich customer/user master** (legal name, contacts, address, company link, consent,
loyalty); **Driver** is the **racing-identity master** (number, nickname, kart class, stats, history,
PR). The "user" is a **composition of owned slices**, not one blob.
→ **Create** can be *initiated from any admin surface* (CRM, POS counter, website) but **always routes
through Identity** for the one canonical id, then fans out. **Read** = every service holds a local copy
of the slices it needs (ECST). **Update** = each service edits only the fields it owns; cross-owned
fields are **read-only event-fed replicas**. **Delete** = the **erasure saga** (`privacy.erasure_requested`,
one coordinated compliant delete). → The two things forbidden — *minting your own id* and *overwriting a
field another service owns* — are exactly what would cause unlinkable duplicates and unresolvable
write-conflicts; their absence is the design working, not a gap.
→ `identity.resolved` deliberately carries only `{requestId, email, masterId}`; **rich user data
propagates from CRM (`crm.person_updated`) and Driver (`driver.profile_updated`)**, not from Identity.

## Round 23 — Security CI: scanning scope, tooling, phasing & enforcement (2026-06-04)

> Decided during the **build phase** (after Story 1.1 scaffolded CI), closing an **open question**
> surfaced while reviewing CI coverage: the corpus specified secure-by-**design** practices (secrets via
> `.env`, none in code/logs/images, CI holds only GHCR+SSH creds, PCI SAQ-A, least-privilege tokens —
> [03-engineering-standards.md §7](03-engineering-standards.md)) but **no automated security *scanning***.
> This is an **engineering-standards addition**, not an architecture change: no service, bus, or
> `/contract` decision is touched, so **no new ADR**. To be reflected in
> [03-engineering-standards.md](03-engineering-standards.md) §7 + the CI table, and built into
> `.github/workflows/` as each language lands.

**Q23.1 — Should Pitwall's CI run automated security scanning at all, and over what scope?**
A: **Yes — all four categories:** (1) **secret scanning**, (2) **dependency / SCA**, (3) **SAST**
(static analysis), (4) **container image scanning**. Secure-by-design covers *how we write code*;
these cover *what slips through anyway* (a committed token, a vulnerable transitive dep, an injection
pattern, a CVE in a base image). For a public portfolio repo the marginal cost is low and the practice
is itself part of what the project demonstrates.

**Q23.2 — Which tools?**
A: **GitHub-native first, plus Trivy for images.**
→ **Secret scanning:** GitHub secret scanning + **push protection** (native); a **gitleaks** CI job is
the portable blocking gate where native GHAS isn't available (e.g. a private repo). *(Open sub-detail,
not assumed: repo visibility / GHAS availability — confirm at enablement; gitleaks is the fallback that
makes the gate work either way.)*
→ **Dependency / SCA:** **Dependabot** (native alerts + update PRs) backed by a per-language vuln check
in CI — **govulncheck** (Go), **pip-audit** (Python), **npm audit** (TS).
→ **SAST:** **CodeQL** (native; Go, Python, JS/TS).
→ **Container images:** **Trivy** scanning the built service images.
*(Rejected for now: third-party SAST like Semgrep, and OSS-only pipelines — GitHub-native is free for a
public repo, lower-maintenance, and reports into the Security tab.)*

**Q23.3 — When does each scan land?**
A: **Phased with the build (walking-skeleton-first), not all up front.** Secret scanning applies
**immediately** (the scaffold can already leak a secret); the language-specific scans arrive **with the
code they scan** — CodeQL/govulncheck/Trivy-on-the-Go-image at **Story 1.3** (first Go service), CodeQL
Python + pip-audit at **3.1**, CodeQL JS/TS + npm audit at the **TS tier (4.x/5.x)**. Dependabot is
enabled once a language manifest exists. Standing up no-op jobs before there's anything to scan is
avoided. Each is **path-filtered** like the rest of CI (AR17).

**Q23.4 — How strict are the gates?**
A: **Blocking on secrets and on high/critical SAST findings; advisory (report-only) for dependency and
image findings.** A detected secret or a high/critical CodeQL finding **fails CI / blocks merge** (no
merge on red — these are the genuinely dangerous classes). Dependency-CVE and image-CVE results
**report to the Security tab but do not block**, so transitive-dep noise can't stall a solo build; they
are triaged via Dependabot PRs on their own cadence. This mirrors the existing "no merge on red" gate
for the dangerous cases while keeping the noisy cases informational.

## Round 24 — Test-Driven Development as the working method (2026-06-04)

> Decided during the build phase (Epic 1 in progress). Confirms *how* code is written, not *what* is
> tested — the four test layers ([03 §3](03-engineering-standards.md)) were already mandated; this pins
> the **order**. An engineering-standards / Definition-of-Done addition — **no ADR** (no service / bus /
> `/contract` change).

**Q24.1 — Is TDD the mandated implementation process for the build?**
A: **Yes — test-first, red → green → refactor, for every story.** Write the failing test(s) **first**,
derived directly from the story's **Given/When/Then** acceptance criteria; watch them fail; write the
*minimum* code to pass; then refactor under the green. No production code lands without a failing test
that motivated it; no merge on red.
→ The design already suits this: the stories are written in Given/When/Then (ready-made test specs), the
four test layers (unit · integration on real RabbitMQ+DB · contract · e2e smoke) are CI-gated, and the
`/contract` valid + known-bad fixtures are themselves test-first artifacts. TDD is the **how** (order);
the layers are the **what**. Recorded in **[03 §3](03-engineering-standards.md)** + the **Definition of
Done** (03 §8 and `CLAUDE.md` §5). **Story 1.1** (scaffold/infra) predates this and is exempt; **from
Story 1.2 onward** it applies. Every `dev-story` session implements test-first by default.

## Round 25 — Heartbeat contract & the Go service skeleton host (2026-06-04)

> Decided during the build phase (Epic 1, **Story 1.3** — the Go service skeleton). Resolves a genuine
> contradiction in the existing docs and an under-specified host choice surfaced while creating the story.
> The heartbeat-`type` decision **touches the wire** (`/contract`), so it is contract-significant; the
> others are build placement. No new ADR (no architectural rule changes — it *removes* an ambiguity within
> the existing envelope rule + bus topology).

**Q25.1 — What is the heartbeat event's `type` / routing key?**
A: **`control.heartbeat`** (entity `control`, action `heartbeat`). The event catalog
([02 §4 `control.events`](02-message-bus-and-contracts.md)) listed the routing key as a bare `heartbeat`,
but the committed `envelope.schema.json` pins `type` to `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$` — an
`<entity>.<action>` form with a **mandatory dot** (Story 1.1). A bare `heartbeat` would fail its own
envelope schema. `control.heartbeat` satisfies the pattern and groups naturally with the other
`control.*` liveness events (`control.selfping`). **Ownership is unchanged:** per the cross-cutting-event
rule ([02 §4](02-message-bus-and-contracts.md), same as `privacy.erased`), each service publishes its own
heartbeat **to its own `<service>.events` exchange** (the `source` field names the emitter); the Control
Room binds with a wildcard and aggregates. The `control.events` catalog row is a *logical* grouping, not
the owning exchange. **Update [02 §4](02-message-bus-and-contracts.md)** to write the routing key as
`control.heartbeat` and to note it is emitted to each service's own exchange.

**Q25.2 — Is the heartbeat schema added to `/contract` now (Story 1.3), and is it validated on publish?**
A: **Yes — add it now and validate-on-publish.** The [service blueprint](04-service-blueprint.md) mandates
validating **every** message in and out against `/contract`, and the repo's standing promise is that every
event ships a valid example **and** a known-bad fixture (proved by `check-invalid-fixtures.py`, Story 1.2).
Story 1.3 therefore adds `contract/schemas/control/heartbeat.v1.schema.json` + a valid `*.example.json` +
a known-bad `*.invalid.json` (data payload: `service`, `at`, `instanceId`), and the Go skeleton validates
each heartbeat envelope **before** publishing. This is distinct from Story 1.4's "validate-on-publish via
the **outbox relay**": the heartbeat is **ephemeral liveness, not outbox-backed** (you never replay a stale
heartbeat), so it is published directly on the 1 s ticker with its own pre-publish validation.

**Q25.3 — Which service directory hosts the inline Go skeleton built in Story 1.3?**
A: **`services/timing/`.** The architecture builds the blueprint machinery **inline** in Epic 1 and
**extracts** `libs/go-pitwall` in Epic 2 ([architecture §"Grow it, don't pre-scaffold it"]). Timing is the
first service to come alive and Stories **1.5** (simulator), **1.6** (lap-validity) and **1.8** (session
lifecycle) build **directly** on this skeleton — so hosting it in `services/timing/` throws nothing away
(grow-don't-pre-scaffold, AR15) rather than building a disposable template service.

## Round 26 — Leaderboard consumer + live trackside board (2026-06-08)

> Decided during the build phase (Epic 1, **Story 1.7** — the first *consumer* service, the first
> non-Timing Go service, with a live web display). Three decisions were genuinely unpinned in the
> existing docs and surfaced while creating the story; per the golden rule they are answered here
> before building. Q26.3 **touches `/contract`** (it decides *not* to add a `driver` schema yet, and
> why). No new ADR — none changes an architectural rule; they resolve under-specified build choices
> within the existing blueprint (ECST read-model, bus-only liveness, grow-don't-pre-scaffold).

**Q26.1 — Live transport for the trackside board: WebSockets or SSE?**
A: **SSE (Server-Sent Events).** Every doc to date wrote the choice as "websockets/SSE"
([01](01-architecture-overview.md), [Round 9 line ~253], architecture §UI) without picking one. The
board is **read-only and one-way** — the Go service pushes standings; the browser never sends a domain
message back — which is exactly SSE's shape. SSE rides plain HTTP (no upgrade handshake), and the
browser `EventSource` gives **built-in auto-reconnect**, which directly serves the Story-1.10
stale/reconnect requirement (FR47). It is trivial to serve from Go's `net/http` alongside the
`//go:embed`-ed Vite/React bundle on the **same port**. This is **not** an HTTP `/health` endpoint and
**not** HTTP polling — it satisfies ADR-0004 (bus-only liveness; live UI via push, never polling). A
future bidirectional surface (e.g. Control Room controls) may still choose WebSockets independently;
this decision is scoped to the read-only Leaderboard display.

**Q26.2 — How is the Leaderboard read-model + idempotent inbox stored in Story 1.7?**
A: **Durable SQLite, mirroring Timing's `persistence/` package** (modernc pure-Go driver, goose
migrations) — a durable **inbox** table (dedupe on the envelope `id`, M6) and a **standings projection**
table. The blueprint lists `persistence/` (private DB + inbox table) as a mandatory part and mandates
**database-per-service** (SQLite everywhere except Billing); building it durable now keeps Leaderboard
consistent with Timing and sets up **Story 1.8** (session keying/reset) and **Story 1.10** (last-processed
marker + replay/reconverge) cleanly, rather than shipping an in-memory model in 1.7 and then introducing
the whole persistence package + a redeclare in 1.10. FR41's "pure read-model rebuilt from events" is
honored: the projection owns **no canonical state** — it is a fold of consumed events and is fully
rebuildable. *(Scope note: the last-processed **marker** and event **replay** themselves are Story 1.10;
1.7 just persists the inbox + projection so they survive a process restart.)*

**Q26.3 — How far does Story 1.7 go on `driver.profile_updated` nicknames?**
A: **Defer the wiring; build tolerant.** AC3 (FR46) requires display names to update "when a
`driver.profile_updated` nickname later arrives — *tolerant of the producer not existing yet*." There is
**no `driver` namespace in `/contract`** and **no Driver service until Epic 3** (Story 3.2), and in Epic 1
the only identifier on `lap.recorded` is `masterId` (racing numbers also do not exist until Driver/Identity
in Epic 2/3). So Story 1.7: (a) renders the fallback display name = a **short `masterId`** (the only
identifier available); (b) **keys each standings row on `masterId`** and treats the display name as a
separate **overlay field**, so a nickname can be applied in place later without disturbing the row's
identity or its lap data; and (c) does **NOT** add a `contract/schemas/driver/*` schema or a
`driver.profile_updated` consumer binding — those land in **Epic 3** when the Driver service owns and
produces that event. This honors the no-invented-schema rule (golden rule + the Story-1.6 precedent:
*don't define another service's wire contract ahead of its owner; flag the gap instead*). The board works
correctly with no nicknames; the overlay seam is designed but unwired. *(`session.started`/`session.ended`
consumption — auto-reset and active/finished status, FR43/FR45 — are likewise **out of 1.7 scope**, owned
by Story 1.8; 1.7 accumulates a single best-lap-per-driver board from `lap.recorded` only.)*

## Round 27 — Poison-message handling: DLQ topology knobs (2026-06-12)

> Decided during the build phase (Epic 1, **Story 1.9** — the first service to wire the consumer-side
> **DLQ + TTL-retry + parking**, which the blueprint mandates for every service). The *design* was already
> pinned in **Q19.5** (TTL-retry via a `<consumer>.<purpose>.retry` queue dead-lettering back to source +
> a delivery-count cap → terminal `<consumer>.<purpose>.parking` quarantine + Control Room alert) and the
> **topology names** in the architecture (DLX `<consumer>.dlx`, retry/parking queues). What was **explicitly
> left open** — and the docs forbid inventing — were: the **queue type** (architecture's "pin classic vs
> quorum **once**" gap) and the **retry cap + backoff TTLs** (epics §"Build-config knobs — *confirm at
> build, not assumed*; recorded as story-level config, not invented values"). These are answered here before
> building. **No new ADR** — they pin under-specified values inside the existing blueprint/ADR-0005 DLQ
> rule, changing no architectural decision.

**Q27.1 — Classic or quorum queues for the work / retry / parking queues (pinned once for the blueprint)?**
A: **Classic queues.** Pitwall runs **single-node** RabbitMQ everywhere (Compose for dev/staging, one VPS
for prod — ADR-0007), so quorum's HA/replication benefits (its main reason to exist) buy nothing here while
costing more RAM against the 7 GB ceiling (Q19.4). The documented backoff is **exponential per hop** with a
**single** `<consumer>.<purpose>.retry` queue, which the chosen mechanism implements via **per-message TTL**
(`expiration` set per republish, doubling each hop) — classic queues support per-message TTL cleanly; quorum
queues do not (they favour a fixed `x-delivery-limit`, which is constant-delay, not exponential). The
delivery-count **cap** is read from the **aggregate `x-death` count** RabbitMQ stamps each time the retry
queue dead-letters back to source (Q19.5 "cap on aggregate x-death"), so no custom counter is invented.
*(Scope: this pins the type for the consumer-side DLQ topology across all services. A future clustered
deployment could revisit, but that is not on the roadmap.)*

**Q27.2 — How many delivery attempts before terminal parking (env knob `DLQ_MAX_ATTEMPTS`)?**
A: **5 attempts** — the original live delivery + **4 retry hops**, then park. Matches the architecture's
worked example ("e.g. 5 attempts") and gives transient faults (a brief DB hiccup, a momentary peer/bus
blip) ample room to clear before a message is quarantined, while still terminating a genuinely-poison
message in bounded time. The cap is evaluated against the aggregate `x-death` count (Q27.1); the
**5th** failed attempt routes to `<consumer>.<purpose>.parking` + a Control-Room-bound alert.

**Q27.3 — Exponential backoff schedule (env knobs `DLQ_RETRY_BASE_MS`, `DLQ_RETRY_MULTIPLIER`, `DLQ_RETRY_MAX_MS`)?**
A: **1 s base, ×2 per hop.** Per-message retry TTL = `min(BASE · MULTIPLIER^(hop-1), MAX)`, giving the
4 retry hops (under the cap of 5 from Q27.2) delays of **1 s → 2 s → 4 s → 8 s** (~15 s total in retry
before parking). `DLQ_RETRY_BASE_MS=1000`, `DLQ_RETRY_MULTIPLIER=2`, `DLQ_RETRY_MAX_MS=60000` (the max is a
blueprint ceiling that does not bind at this cap but caps growth for services that later raise the attempt
count). Balanced for live timing data: fast enough to reconverge on a brief blip, slow enough not to hammer
a struggling downstream.

> **Two carry-through clarifications (determinable from the docs, recorded for the dev agent — not open
> questions):** (a) an **invalid** message (fails validate-on-consume against `/contract`, or carries a
> blank `sessionId`) is **logged + sent straight to `parking` + alerted — never entered into the retry
> loop** (AC3 / M5: "not retried as poison"; retrying malformed bytes can never fix them, mirroring the
> producer-side `quarantined` rationale). (b) The **alert** is, until Control Room (E12) exists, a
> **structured ERROR log line** with a stable marker (no new `/contract` event is invented ahead of its
> owner — golden rule + the Story-1.6/1.7 precedent); the parking queue itself is the durable, observable
> quarantine.

## Round 28 — Cross-language conformance harness + e2e smoke: execution model & layout (2026-06-13)

> Decided during the build phase (Epic 1, **Story 1.11** — the cross-language conformance harness + green
> e2e smoke gate). The *design* was already pinned: **AR16** ("one YAML scenario spec — publish-redeliver,
> inbox-dup, crash-after-ack, peer-down — + a thin **per-language runner (Go for now)** asserting identical
> observable outcomes against the **same RabbitMQ**; it **IS the e2e skeleton**; tests **wait on observable
> conditions, never `sleep`**; flaky → **quarantine lane, never `@skip`**"), **NFR23** (four CI-gated test
> layers; e2e smoke is a **required merge gate**, no merge on red), and **engineering-standards §3.4** (the
> e2e smoke is the happy path across the bus **using the simulators**). What was **not pinned anywhere** —
> and the docs forbid inventing — was the harness's **execution model** (does the runner drive the real
> built service *artifacts*, the real *binaries*, or wire the packages *in-process*?) and its **repo
> layout/module shape**. Both are answered here before building. **No new ADR** — this realizes AR16's
> harness inside the existing architecture, changing no architectural decision; it sets the e2e *pattern*
> every later epic inherits ("the Epic-1 e2e smoke + 4-layer CI stay green").

**Q28.1 — How does the conformance harness / e2e smoke execute the system under test?**
A: **Real service binaries against a real (testcontainers) RabbitMQ.** The Go runner reads the YAML scenario
spec, stands up a real `rabbitmq:4.3-management-alpine` via **testcontainers** (the same broker every
scenario shares), `go build`s the **real Timing (simulator mode, seed-deterministic) and Leaderboard
binaries**, runs them as **subprocesses** pointed at that broker via env, and asserts **observable
outcomes** — the served board (Leaderboard HTTP/SSE snapshot), outbox/queue state (RabbitMQ management API),
and structured log markers — **waiting on conditions, never sleeping**. Rationale: (1) it is **genuinely
end-to-end** — real entrypoints, real config loading, real wiring, real broker — not a re-test of the same
in-process seams the integration layer already covers; (2) it **matches the existing CI shape** (`ci.yml`
uses `setup-go` + testcontainers and does **not** build Docker images — image builds live in `release.yml`
per **AR17**), so the smoke gate adds no image-build cost to every PR; (3) it cleanly expresses all four
AR16 reliability scenarios at the right seam (peer-down = Stop the broker container, as Story 1.10 proved;
crash-after-ack = kill+restart the consumer subprocess; inbox-dup = republish a persistent message;
publish-redeliver = nack/redeliver); (4) "**thin per-language runner**" is honoured — the runner is Go
*test code* driving real artifacts, and a second-language service later joins the **same YAML spec** with
its own thin runner. *Rejected:* **`docker compose up` the images** (most prod-faithful, but forces every PR
to build the Timing+Leaderboard images — the Leaderboard image has a node/vite stage — which the current
per-PR pipeline deliberately does not do; revisit only if subprocess-vs-image fidelity ever bites);
**in-process package wiring** (lightest, but the services are **separate Go modules** so it needs
`go.work`/`replace`, and it is **not truly e2e** — it just re-exercises the integration seam).

**Q28.2 — Where does the harness live, and is the Go runner its own module?**
A: **`tests/conformance/`, with the Go runner as its own Go module.** Layout: `tests/conformance/scenarios/*.yaml`
(the **language-neutral** scenario spec) + `tests/conformance/go/` (its **own `go.mod`**, so it can depend on
amqp091 / testcontainers / a YAML parser without touching the service modules; it drives the binaries built
from `services/*`, it does **not** import their internal packages). This sits parallel to the existing
`tests/contract/` (Python). **One** directory — not a `tests/e2e/` + `tests/conformance/` split — because
AR16 states the conformance harness **IS the e2e skeleton**: the happy-path e2e smoke is simply the
`smoke` scenario alongside the four reliability scenarios in the same spec. Wiring: **`make smoke`** runs it;
a **new required `ci.yml` job (`e2e-smoke`)** gates merges (NFR23). A **flaky** scenario moves to a
**quarantine lane** (a non-required, separately-reported run — e.g. a `quarantine`-tagged job), **never
`@skip`** (AR16). *(Scope: this pins the harness location + module shape + the `make`/CI entrypoints for the
whole platform; later languages add a sibling runner under `tests/conformance/<lang>/` against the same
`scenarios/*.yaml`.)*

## Round 29 — VPS deploy: trigger mechanism, GHCR visibility & prod board reachability (2026-06-14)

> Decided during the build phase (Epic 1, **Story 1.12** — deploy the walking skeleton to the VPS). The
> *design* was already pinned: **ADR-0007** (monorepo; **per-service tags `‹svc›-vX.Y.Z`** build → **GHCR**
> → **VPS pulls** + recreates only that container; deploy ≠ merge; rollback = redeploy the previous
> immutable GHCR tag, no server rebuild) and **engineering-standards §6** ("CI holds only GHCR + SSH deploy
> creds"; "Secrets: `.env` on the VPS"). What was **not pinned anywhere** — and the docs forbid inventing —
> was the concrete *deploy trigger mechanism* (push-from-CI vs VPS-pull poller), the *GHCR package
> visibility*, and *how the prod leaderboard board is "reachable"* on a VPS that has **no reverse proxy, no
> domain, and no public web port** and is **shared with another live project** (AI-bot: `rules_bot_*` +
> `dozzle`; `127.0.0.1:8080`/`8081` already taken). All three are answered here before building. **No new
> ADR** — this realizes ADR-0007's pipeline within the existing architecture, changing no decision.
>
> **Standing constraint (Jeremy, this round):** the repo is **public**, so **no secret may ever be
> committed — especially the VPS IP**. Repo artifacts use placeholders / CI-or-VPS-side secrets only; the
> host lives solely in the VPS-side gitignored `.env` and the operator's local tooling.

**Q29.1 — What triggers the per-service deploy: CI pushes (SSH) or the VPS pulls (poller)?**
A: **VPS-side pull poller** (reuse the proven pattern already running the other project). A flock-guarded
script on a **systemd timer** (mirroring the existing `rules-deploy.timer` → `rules-deploy.service`) polls
`git fetch --tags`, finds the newest `‹svc›-vX.Y.Z` tag, and for the changed service runs **`docker compose
pull <svc>` + `up -d <svc>`** against the GHCR image (no `--build`: images are built in CI, never on the
server — ADR-0007). Per-service deployed state is tracked in a `.deployed_tags` marker. Rationale: (1) it is
**pull-based, so CI needs no inbound SSH and no SSH secrets** — the single biggest secret-leak risk on a
public repo is removed (satisfies the standing constraint above), and the "CI holds … SSH deploy creds" line
in eng-standards §6 is **superseded for the poller path** (CI holds only GHCR push, which is the automatic
`GITHUB_TOKEN`); (2) it **matches infrastructure the operator already runs and trusts** on this exact VPS;
(3) it still satisfies ADR-0007 literally — "the **VPS pulls** + recreates only that container". *Rejected:*
**CI SSHes into the VPS** (push-based, immediate, but requires storing an SSH deploy key as a repo secret on
a public repo and an inbound trust path — more attack surface for no benefit here).

**Q29.2 — Are the GHCR images public or private?**
A: **Public packages.** Built as `ghcr.io/jeremy-luyckfasseel/pitwall-<svc>`. The repo is already public, so
the VPS pulls **with no `docker login`** — fewer secrets, fewer moving parts, and appropriate for an open
portfolio project. *Rejected:* private packages (would force a `read:packages` PAT onto the VPS for no
confidentiality gain here).

**Q29.3 — How is the production leaderboard board "reachable"?**
A: **Loopback-only on the VPS, reached via an SSH tunnel** — no public surface. The board binds to
`127.0.0.1:<port>` on the VPS (same posture as the existing `dozzle`/dashboard), and the operator views it
by tunnelling (`ssh -L`). The board is **read-only and unauthenticated** (Round 26 — the browser never sends
a domain event), so exposing it publicly is an unnecessary risk; loopback keeps the attack surface at zero
and changes no firewall rule. A **Cloudflare Tunnel** (free, outbound-only, would give a stable HTTPS URL
with no inbound ports) was the preferred *production-like* option and is the documented **future upgrade**,
but it needs a Cloudflare account + a zone the operator does not currently want to set up; **safest wins for
the walking skeleton.** *(AC note: the deploy story's "live board is reachable in prod" is satisfied via the
SSH tunnel; "bus-only health" is unchanged — heartbeats + the touch-file Docker healthcheck, no HTTP
`/health`.)*

**Q29.4 — Which host port does the prod board bind, given `8080`/`8081` are taken by the other project?**
A: A **non-colliding loopback port** in the prod overlay (`docker-compose.prod.yml`), leaving the
in-container `:8080` unchanged — the base file's `127.0.0.1:${LEADERBOARD_HTTP_PORT:-8080}:8080` is
overridden on the VPS via `LEADERBOARD_HTTP_PORT` in the VPS `.env` so it never clashes with `dozzle`
(`8080`) or the AI-bot dashboard (`8081`). The exact value is VPS-`.env` config, not committed.

## Round 30 — Identity resolve-or-mint: contract placement & test scope (2026-06-15)

> Decided during the build phase (Epic 2, **Story 2.2** — Identity resolves or mints exactly one
> `masterId` per person). The *design* was already pinned: **FR1–FR3 / NFR15** (Identity is sole issuer,
> de-dupes on email, stores only `masterId`+email+status+timestamps), **ADR-0003** (Identity as UUID
> issuer), and the **`identity.resolved` schema** + the message-bus rule that **`.requested` intents are
> published to the originating service's own exchange** (`02-message-bus-and-contracts.md` §"Note: intents
> … originating service's own exchange"; `contract/README.md` Events rule). What was **not pinned** — and
> the docs forbid inventing — were two build-time specifics surfaced by create-story for 2.2: (1) which
> `/contract` namespace folder the **new** `identity.lookup_requested.v1` schema+example lives in (the tree
> is mixed — `frontend/` holds the frontend-originated intents, but `privacy/privacy.erased` is filed
> by-entity even though its source varies, and the message-bus catalog lists `identity.lookup_requested`
> under a *logical* `### identity.events` grouping); and (2) how far 2.2's "e2e" test layer reaches given
> the cross-language conformance harness today launches only the Timing+Leaderboard binaries. **No new ADR**
> — these realize the existing identity design and contract rules, changing no decision.

**Q30.1 — Which `/contract` namespace folder holds the new `identity.lookup_requested.v1` schema + example?**
A: **`contract/schemas/frontend/` + `contract/examples/frontend/`.** It is filed with the other
frontend-originated `.requested` intents (`profile.edit_requested`, `schedule.change_requested`,
`session.control_requested`, `privacy.erasure_requested`, `privacy.export_requested`). This matches the
normative rule that intents are published to the **originating** service's own exchange (`frontend.events`),
which Identity binds its consumer queue to — so the folder reflects the physical publishing exchange. The
test producer (a Frontend stand-in, since Frontend itself is Epic 5) emits the envelope with
`source: "frontend"`. The message-bus catalog's `### identity.events` row for `identity.lookup_requested` is
a **logical grouping, not the physical exchange** (same caveat the catalog already makes for `control.events`
and `privacy.*`). The reply `identity.resolved` stays under `identity/` (it *is* published to
`identity.events`, source `identity`). *Rejected:* filing the request under `identity/` to keep the
request/reply pair co-located — appealing, but the folder would then misrepresent the publishing exchange and
break the consistent "frontend intents live in `frontend/`" precedent. Counter (Bar/POS) walk-in
registration (Q22.3, Epic 7) will publish the *same* `identity.lookup_requested` schema to `bar.events`;
the single schema file is referenced from both — its folder marks the **primary** origin, not the only one.

**Q30.2 — How far does Story 2.2's "e2e" test layer reach?**
A: **Integration-only for 2.2; keep the existing conformance smoke green.** Resolve-or-mint is covered by
**thorough per-service integration tests** (real RabbitMQ + SQLite via testcontainers): mint-on-unknown
email, reuse-on-known email (no duplicate, no `isNew`), redelivery/replay idempotent (same email → same
`masterId`), concurrent same-email race → exactly one `masterId` (unique-constraint single-writer),
malformed lookup → log + dead-letter (DLQ), and bus-bounce survival via the outbox. The existing
**Timing→Leaderboard cross-language conformance smoke stays green** — Identity is purely additive to it and
is **not** wired into the conformance harness in this story. The harness is extended in **Story 2.3** (gate
check-in), which is the first time Identity actually *chains* into a multi-service observable flow; doing it
there avoids building harness scaffolding in 2.2 that 2.3 would rework. *Rejected:* teaching the conformance
harness to launch the Identity binary + adding an identity scenario now (premature — no multi-service
identity flow exists until 2.3).

## Round 31 — Identity email natural-key: normalization & wire validation (2026-06-18)

> Surfaced by the **Story 2.2 code review** (three adversarial layers converged). The design pins Identity as
> de-duplicating on **email** to issue **exactly one `masterId` per person** (FR1/FR2/FR3/NFR15, ADR-0003),
> but **no document specified the canonical form of that email** nor how its shape is enforced on the wire. As
> built, Identity keyed `UNIQUE(email)` on the raw string (SQLite binary, case-sensitive) and stored the value
> untrimmed, so `Jeremy@x.com`, `jeremy@x.com`, and `" jeremy@x.com "` would mint **different** `masterId`s —
> contradicting AC2 ("exactly one canonical id per person"). Separately, the `email` field carried only
> JSON-Schema `format: "email"`, which is **annotation-only (not asserted)** in the repo's validator (see
> `libs/go-pitwall/messaging/validate_test.go` — "format is annotation-only, so this must be caught by the
> pinned pattern"), so `"xxx"` validated and could be stored as a person's natural key. Per the golden rule
> (CLAUDE.md §0) these unspecified behaviors were **asked, not assumed**. **No new ADR** — this realizes the
> existing FR1–FR3 / NFR15 de-dup intent and the `contract/README.md` "validate every message" rule; it
> changes no architectural decision.

**Q31.1 — What is the canonical form of the email natural key, and where is it normalized?**
A: **Identity normalizes — trim surrounding whitespace and lowercase the entire address — before it
de-duplicates, stores, and echoes the value.** Identity is the **authoritative** point of normalization: it
does not trust producers to send a canonical form (the lookup can originate from Frontend online registration,
and later the Bar/POS counter walk-in — Q22.3/Epic 7 — so relying on every producer to normalize is fragile).
The normalized form is used as the single key for both the `INSERT … ON CONFLICT(email)` and the post-conflict
`SELECT`, is the value persisted in `identities.email`, and is the `data.email` echoed in `identity.resolved`.
Consequence: case- and whitespace-variant addresses for one person resolve to **one** `masterId`, satisfying
AC2. *Rejected:* (a) a wire-contract rule mandating senders submit trimmed-lowercase email — pushes the
guarantee onto every current and future producer and still needs a defensive normalize in Identity, so it adds
a cross-language wire rule for no extra safety; (b) trim-only / case-sensitive (RFC-5321-strict local part) —
technically defensible but wrong for this platform, where one human with one mailbox must be one person.

**Q31.2 — How is email *shape* enforced on the wire (since `format` is annotation-only)?**
A: **Add a pragmatic email `pattern` to the schema** (`^[^@\s]+@[^@\s]+\.[^@\s]+$` — a non-empty local part,
an `@`, and a dotted domain, no embedded whitespace) on the `email` field of **both**
`frontend/identity.lookup_requested.v1` and `identity/identity.resolved.v1`, plus a known-bad fixture whose
**email** is the sole reason for rejection (a valid `requestId`, a malformed email) so the pattern is proven to
bite. This mirrors how the corpus already pins `requestId`/`masterId` via an explicit `pattern` rather than
relying on `format`. The pattern is **case-tolerant** (allows uppercase) because validate-on-consume runs on
the **raw** envelope *before* Identity normalizes — the producer may legitimately send `Foo@X.com`. *Rejected:*
enabling format-assertion globally in the shared validator — correct in spirit but a corpus-wide, cross-language
behavior change with a large blast radius; out of scope for a Story 2.2 review fix and better raised as its own
decision. Also pinned: `identity.resolved.masterId` now carries the strict **v4** `pattern`
(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`), matching the `lap.recorded`
precedent and AC1's "lowercase UUID-v4" requirement (it previously had `format: "uuid"` only).

## Round 32 — Story 2.3 gate check-in: Identity chaining, transponder-map scope & the `driver.checked_in` payload (2026-06-18)

> Surfaced while drafting **Story 2.3** (QR-embedded `masterId` + gate check-in). Three decisions were
> **not** settled by any existing doc/ADR/Q&A, so per the golden rule (CLAUDE.md §0) they were **asked, not
> assumed**. Context: the Timing simulator (Story 1.5) currently mints **fixture** `masterId`s and goes
> straight to laps — its own code comment forecasts "real Identity-issued ids replace them in Epic 2."
> **Q30.2** already pinned Story 2.3 as "the first time Identity actually chains into a multi-service
> observable flow." **No new ADR** — this realizes the existing FR4/FR32 check-in design (timing.md Round 6,
> Q5.3/Q6.1–Q6.3) and the Epic-2 plan; it changes no architectural decision.

**Q32.1 — In Story 2.3, how does the gate check-in obtain the canonical `masterId`?**
A: **The simulator resolves each driver via Identity before check-in.** For every fixture driver the
simulator publishes `identity.lookup_requested {requestId, email}` and consumes `identity.resolved`, then uses
the **real Identity-issued `masterId`** for `driver.checked_in` and all subsequent `lap.recorded` — replacing
the locally-minted fixture ids (exactly the Epic-2 swap the simulator's own comment forecasts). This adds an
`identity.resolved` **consumer** to Timing (timing.md already lists Timing consuming `identity.resolved` "when
registering a walk-in token"). The **conformance harness is extended** (per Q30.2) to run Identity + Timing +
Leaderboard and assert the `masterId` that came out of `identity.resolved` is the **same** id that appears in
`driver.checked_in` and on the Leaderboard board — proving the canonical-id chain end-to-end. *Scope guard:*
this is the **happy register-then-check-in chain only**; register-**first enforcement** (a lap is never
emitted for an unresolved token) and the **unknown-token-at-the-line operator exception** remain **Story 2.5**.
*Rejected:* keeping the simulator on fixture ids and proving the link only in an isolated harness scenario —
the simulator would still mint its own ids, so Identity would not actually be chained into Timing's real
output, defeating Q30.2's intent and deferring the unavoidable fixture→real-id swap with no benefit.

**Q32.2 — Story 2.3 needs transponder→`masterId` *resolution* (AC2), but the mapping's *assignment at
hand-out* is Story 2.4. What lands in 2.3?**
A: **2.3 builds the transponder→`masterId` map store + the gate resolution read path; the hand-out
assignment trigger lands in 2.4.** Timing gains a local `transponder_map` store (its own DB slice, keyed on
the hardware id) with a direct upsert seam used by tests/seeding, plus the **resolution** logic: a gate scan
carrying a `transponderId` is resolved to its `masterId` via the map before `driver.checked_in` is emitted
(`checkInMethod: "transponder"`). The **assignment-at-hand-out trigger, latest-wins reassignment, and its
logging** are **Story 2.4** (it wires *when* a mapping is written; 2.3 owns the store + the read). A scan for a
transponder **absent** from the map is **not** minted/guessed — it is the register-first/unknown-token concern
deferred to **Story 2.5** (in 2.3 the simulator only ever checks in transponder drivers whose mapping it has
seeded). *Rejected:* QR-only in 2.3 (moving all transponder handling to 2.4) — that drops 2.3's stated AC2 and
splits one cohesive read/write feature awkwardly across two stories.

**Q32.3 — What is the `timing/driver.checked_in.v1` data payload (a new wire event)?**
A: `data: { masterId (required, lowercase UUID-v4 pattern), at (required, RFC3339 UTC, 3-digit ms, literal
Z), checkInMethod (required, enum ["qr","transponder"]), transponderId (string|null) }`, with
`additionalProperties: true` (tolerant reader / additive evolution, never `additionalProperties:false`). This
mirrors the `lap.recorded` precedent (same strict `masterId`/`at` patterns; `transponderId` nullable and
**always present** — null for QR drivers). `checkInMethod` records **how** the driver was identified at the
gate (QR-direct vs transponder-resolved). **No `sessionId`** — check-in marks presence at the entry gate and
is not itself session-scoped (a tab opens on check-in, Q11.x/Epic 7; laps carry their own `sessionId`).
Published to **`timing.events`** with routing key `driver.checked_in`. A valid example **and** a known-bad
fixture ship with the schema (AR12). *Rejected:* a bare `{masterId, at}` (loses the QR-vs-transponder
provenance a future consumer/audit wants, and adding it later is a schema change); adding `sessionId` now
(check-in is gate-scoped, not session-scoped — would invent an unmodeled coupling).

## Round 33 — Story 2.6 Identity erasure: does the tombstone suppress a same-email re-mint? (2026-08-01)

> Surfaced while drafting **Story 2.6** (Identity erasure handler and tombstone). `libs/go-pitwall/erasure`
> (built ahead of any consumer in Story 2.1) deletes a service's slice and writes a tombstone in one
> transaction, keyed on `masterId` — but Identity's own slice **is** the email↔`masterId` mapping, and AC2
> requires that "a late/replayed `identity.lookup_requested` for the same email" not silently re-mint a fresh
> identity. Full deletion of the email row (Q16.4's "every other service fully deletes") and blocking a future
> lookup for that same email are in direct tension: deleting the email is exactly what would make a later
> lookup look like a brand-new person. Nothing in ADR-0009/Q16.3/Q16.4 anticipated this — they were written
> before any service's natural key doubled as its own erasure target. Not answered anywhere → asked per the
> golden rule (CLAUDE.md §0). **No new ADR** — this specializes ADR-0009's existing erasure mechanics for
> Identity's specific SoR shape; it changes no architectural decision.

**Q33.1 — After Identity erases a person, what happens when a NEW `identity.lookup_requested` later arrives
for that same email (not a redelivery of the same envelope — Story 2.2's existing idempotent inbox already
fully no-ops an exact redelivery, including suppressing its reply, for free)?**
A: **Suppress via an irreversible email hash, held exactly like Story 2.5's unknown-token pattern.** On
erasure, Identity deletes the plaintext email from `identities` (true GDPR delete, satisfies Q16.4) but writes
a **SHA-256 hash of the normalized email** (Round 31's `NormalizeEmail` — trim+lowercase — applied first, so
hash equality matches exactly the same set of addresses the live lookup path already treats as one identity)
into a small suppression record alongside the `masterId` tombstone. A later `identity.lookup_requested` whose
normalized-email hash matches a suppressed entry is **held**: durably persisted (never dropped, survives a
restart), logged at alert severity, and Identity **never mints, never replies** — structurally the same shape
as Story 2.5's `HeldLineScanStore` (hold + persist + flag, no new bus event). The requester's own sad path
handles the missing reply (same "requester's sad path handles the timeout" convention already documented for
a malformed lookup). Getting back in after erasure requires an explicit, future, out-of-band capability — out
of scope here. *Rejected:* no suppression at all (fully delete the email row, let a later lookup for that
address mint a brand-new `masterId` with no special handling) — simpler and needs no new mechanism, but does
not satisfy AC2's literal wording ("a late/replayed lookup... would re-mint it... the tombstone is honored"),
and would let an erased person's email silently become live again with zero record that erasure ever
happened for that address, undermining the audit trail ADR-0009/Q16.6 requires for privacy actions.

## Round 34 — Story 3.1 Python skeleton: FastAPI's role & AMQP concurrency model (2026-08-02)

> Surfaced while drafting **Story 3.1** (Python service skeleton, `make contract` codegen, `libs/py-pitwall`).
> The architecture doc's tech-stack table names "Python 3.14.x (FastAPI 0.136.x / Pydantic v2)" for the
> records tier, but CLAUDE.md §2 rule 2 forbids HTTP `/health` endpoints and rule 1 forbids inter-service
> HTTP entirely — Driver has no HTTP surface of any kind. Nothing in the architecture doc or Q&A explains
> what FastAPI is actually *for* on a bus-only service, and no round picked the AMQP client's concurrency
> model (sync vs asyncio) for the Python blueprint. Not answered anywhere → asked per the golden rule
> (CLAUDE.md §0). Jeremy deferred to the dev agent's recommendation on both. **No new ADR** — these are
> build-config/library choices within the already-ratified Python tier (Q19.4), not new architectural
> decisions.

**Q34.1 — What is FastAPI's actual role in a Python service that never serves HTTP?**
A: **Pydantic v2 only — no FastAPI runtime.** `libs/py-pitwall` and Driver depend on `pydantic` directly
(the piece Q19.4/architecture actually needs — DTOs, envelope models, validator ergonomics) and do **not**
instantiate a `FastAPI()` app or run `uvicorn`. FastAPI stays an available-but-unused transitive capability
for if/when a Python service ever legitimately needs an HTTP surface (none do today; every non-negotiable
in CLAUDE.md §2 forbids it for inter-service and health purposes). *Rejected:* mounting a bare `FastAPI()`
app with zero registered routes just to have the framework "in use" — technically matches the architecture
table's wording but adds a live ASGI app + a second process/thread (uvicorn) with no caller and no purpose,
pure ceremony for a bus-only service.

**Q34.2 — What concurrency model should the Python blueprint's AMQP connection use?**
A: **Sync, blocking (`pika`).** Mirrors the Go services' one-goroutine-per-queue blocking-consumer mental
model (Timing/Identity since Epic 1), which the whole platform's messaging/outbox/inbox/DLQ design already
assumes (process one message, ack, move on — no concurrent-delivery reasoning needed). Simpler to test and
reason about for a solo build; avoids threading async/await through outbox, inbox, heartbeat, and erasure
mechanics for zero current benefit (no Python service has a latency/throughput need that requires asyncio).
*Rejected:* `aio-pika`/asyncio — would fit better if FastAPI were actually serving async HTTP (Q34.1 rules
that out) and adds real complexity (async DB access, async test fixtures) with no offsetting requirement.
