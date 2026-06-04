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
