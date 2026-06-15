# Leaderboard

The live trackside standings service (Go) — the walking skeleton's first **consumer**
(Stories 1.7–1.9). It consumes Timing's `lap.recorded` **and `session.started` /
`session.ended`** events off the **`timing.events`** exchange, dedupes them through a
**durable idempotent inbox**, validates every message against `/contract` **on consume**,
folds them into a **session-keyed best-lap standings** read-model with an
**active/finished** session status, and serves a **live trackside board**: a
`//go:embed`-ed Vite/React bundle pushed to the browser over **SSE** (never HTTP polling,
ADR-0004). It is a **pure read-model** — it owns no canonical state (FR41) and is a fold
of consumed events.

The inline blueprint blocks here (config, logging, heartbeat, messaging, persistence) are
**duplicated** from `services/timing` by design; they are **extracted** to
`libs/go-pitwall` in **Story 2.1** (grow-don't-pre-scaffold). The consumer-side
**DLQ/TTL-retry/parking** is wired here (**Story 1.9**, below); the stale flag +
replay-from-marker (**1.10**) builds on this.

## Session lifecycle (Story 1.8 — FR43/FR45/NFR24)

Each session ever seen gets a row in a **`sessions`** table with a **locally-assigned
monotonic epoch** (order of first sight — NOT a wire field) and a **forward-only status
gate**: `implicit → active → finished`. The standings are keyed **per
`(session_id, master_id)`**, so:

- **Auto-reset is non-destructive** (FR43): a new `session.started` becomes the current
  board because it holds the highest epoch — the display switches to it (it simply has no
  rows yet). No consume path ever deletes standings rows.
- **Status** (FR45): the board shows **active** from `session.started` and **finished**
  from `session.ended` (final standings stay on display until the next session). The
  stored `implicit` gate renders as *active*.
- **Out-of-order tolerance** (NFR24): a `lap.recorded` arriving **before** its
  `session.started` implicit-creates the board under the lap's `sessionId` and the late
  start reconciles it (`implicit → active`) without touching its laps; a **replayed**
  `session.started` is an inbox no-op (same envelope `id`) or an idempotent upsert (fresh
  `id`) — it never wipes a live board, never reopens a `finished` session, never moves an
  epoch. A `session.ended` for an unknown session is recorded as `finished`, never dropped.

## What it owns (today)

- Declares and owns the durable **`leaderboard.events`** topic exchange and emits a
  **1 s `control.heartbeat`** to it (bus-only liveness — no HTTP `/health`, ADR-0004),
  validated against `/contract` before publishing.
- A durable queue **`leaderboard.lap-recorded`** bound to the **producer's**
  `timing.events` exchange on routing keys `lap.recorded`, `session.started`, and
  `session.ended` (a service consumes from the producer's exchange and publishes only to
  its own; ONE queue preserves the producer's publish order across event types — the
  queue *name* predates the session bindings and is kept to avoid orphaning a durable
  queue). Consumed with **manual ack** and a bounded QoS prefetch.
- An **idempotent inbox** (private SQLite, `DB_PATH`, on the `leaderboard-data` volume):
  dedupe on the envelope **`id`** (a redelivered lap **or session event** is a **no-op**,
  M6). The dedupe-check, the projection/lifecycle upsert, and the inbox insert commit in
  **one transaction**, and the message is **acked only after** that commit (NFR6) — the
  consumer-side mirror of Timing's transactional outbox.
- A **standings projection**: one row per `(session_id, master_id)` holding the driver's
  current **best lap in that session**. Ordering is **best lap ascending**; ties are
  broken by **whoever set the time first** (earlier wire `at`, then ingest sequence for a
  deterministic, non-flapping order) — FR44.
- The **`sessions`** lifecycle table (epoch + forward-only status) described above; the
  served board is always the **highest-epoch** session with its ordered bests.
- A **live board over SSE**: a `net/http` server on `HTTP_ADDR` serves the embedded SPA at
  `/` and pushes the standings snapshot to every connected client at `/events` on connect
  and on each read-model change (≤2 s convergence target, M7). This HTTP server is the
  **display, not a health endpoint** (liveness stays the touch-file).
- **Validate-on-consume + DLQ (Story 1.9)**: a failure is **logged + dead-lettered, never
  silently dropped**. An **invalid** message (fails `/contract`, undecodable, or blank
  `sessionId`) is parked **immediately**; a **processing** failure is retried with
  exponential backoff and, on exceeding the cap, parked + alerted. See *Poison-message
  handling* below.
- Logs **structured JSON** (one correlationId per process; the per-message correlationId
  threads onto apply/reject logs) and shuts down **gracefully** on SIGTERM/SIGINT.

> **Display names:** Epic 1 has no nicknames or racing numbers, so a row's display name
> falls back to a **short `masterId`**. Each row keys on `masterId` with the display name as
> a separate **overlay** (the `display_name` column, NULL today); a `driver.profile_updated`
> nickname can set it in place in **Epic 3** — 1.7 adds **no** `driver` schema or binding
> (Q&A Round 26, "tolerant of the producer not existing yet").
>
> **Out of scope (deliberately not here):** the stale/reconnecting flag +
> replay-from-marker (**1.10**), and the `libs/go-pitwall` extraction (**2.1**).

## Poison-message handling (Story 1.9 — NFR4/NFR6, M5)

The consumer-side **DLQ topology** (classic queues — Q&A Round 27) so a bad message is
**retried then quarantined, never silently dropped or looped forever**:

- **`leaderboard.dlx`** — the consumer's dead-letter exchange (direct), distinct from the
  `leaderboard.events` heartbeat exchange.
- **`leaderboard.lap-recorded.retry`** — a no-consumer queue; a failed message is republished
  here with a **per-message TTL** (exponential backoff `1 s → 2 s → 4 s → 8 s`) and
  dead-letters back to the work queue when the TTL fires. The retry hop is carried in the
  `x-pitwall-retry-count` header.
- **`leaderboard.lap-recorded.parking`** — the **terminal** quarantine (no further requeue).
  A parked message carries an `x-pitwall-park-reason` header and a structured **alert** log
  line (`alert=message_parked`) — the Control-Room-bound signal (placeholder until E12).

The work queue is (re)declared with `x-dead-letter-exchange=leaderboard.dlx` as a safety net;
the classic-queue immutable-args constraint is **self-healed** (delete + redeclare on a `406`).
The **retry cap (5 attempts) and backoff** are env knobs (below), pinned in **Q&A Round 27** —
this is **not** the producer-side outbox `quarantined` status (a different failure domain).

### Sad-path table

| Failure | Outcome (graceful, "no computer says no") |
|---|---|
| Message fails `/contract` validate-on-consume (bad envelope/data) | **Parked immediately** (`contract-invalid`) + alert — never retried as poison (M5) |
| Valid envelope, **blank `sessionId`** | **Parked immediately** (`blank-session-id`) — cannot key a read-model |
| Transient processing failure (DB hiccup, brief peer outage) | **Retried** with exponential backoff; clears within the cap → applied **exactly once** (idempotent inbox) |
| Persistent processing failure (genuine poison) | Retried to the cap, then **parked** (`retries-exhausted`) + alert — terminated, not looped |
| Broker hiccup mid-republish (retry/park publish fails) | Original **not acked** → requeued, never lost (NFR6) |
| Duplicate / replayed message | Inbox no-op, acked (M6) — unchanged |
| **Mid-session bus kill** (RabbitMQ down) | Board **freezes on last-known**, served bundle flips **stale/reconnecting**; consumer re-dials with backoff — no crash, no fakery (FR47/C1) |
| **Bus restored** | Consumer reconnects, **stale flag clears**, board **reconverges ≤ 10 s** (M9) |
| **Service (process) restart** | Durable read-model + idempotent inbox replay past the marker — **no double-count (M6), no loss (M4)** |

## Bus-outage behaviour (Story 1.10 — FR47/C1, NFR1/NFR2/NFR5/NFR10, M4/M6/M9)

The board **degrades honestly** through a mid-session RabbitMQ outage — flagged stale, never
faked live:

- **On a bus kill**, the consumer's broker connection drops. amqp091-go does **not** auto-recover,
  so a built-in **reconnect supervisor** detects the drop and re-dials with capped-exponential
  backoff (500 ms → 5 s ceiling), re-declares the exchange + DLQ topology and re-subscribes —
  pumping deliveries into a **stable channel** so the consumer never restarts. While down, the
  served SSE bundle carries **`stale:true` / `connection:"reconnecting"`** and the board **freezes
  on last-known standings** (no wipe, no crash). The trackside SPA shows the calm amber
  *"Showing last-known · reconnecting…"* pill (dot + text, never color alone, reduced-motion-safe).
- **On restore**, the supervisor reconnects, the stale flag clears, and the board **reconverges
  within ≤ 10 s** of broker-ready: `Leaderboard.read_model == fold(Timing.event_store)` for the
  session (every lap reflected, ordering correct, M9).
- **On a service (process) restart**, the read-model is **durable** (SQLite) — it is not rebuilt
  from scratch. The **idempotent inbox** is the last-processed marker and the **durable work queue**
  redelivers the unacked tail, so a restart replays past the marker with **no double-count (M6)**
  and **no lost events (M4)**. The tie-break sequence stays monotonic via `MaxSeq` restart seeding.

Reconnect backoff is a documented default (not a confirm-at-build knob — epics §"Build-config knobs").

## Events

| Direction | Event | Exchange / routing key | Notes |
|---|---|---|---|
| out | `control.heartbeat` | `leaderboard.events` / `control.heartbeat` | cross-cutting liveness; payload `service`, `at`, `instanceId` |
| in  | `lap.recorded` | `timing.events` / `lap.recorded` | validated on consume, deduped on envelope `id`, folded into the session-keyed best-lap standings |
| in  | `session.started` | `timing.events` / `session.started` | auto-resets the board (new current session, status **active**); replays reconcile, never wipe |
| in  | `session.ended` | `timing.events` / `session.ended` | status **finished** (final standings stay up); only `sessionId`+`endedAt` are read — `summary[]` items are intentionally unpinned in v1 |

Publishes no domain event (the optional `leaderboard.updated` in the service design is **not**
in scope). The live board is served over **SSE**, not the bus.

## Run

```sh
# whole stack (from repo root): RabbitMQ → Timing (simulator on) → Leaderboard
docker compose up -d --build

# Open the live board (loopback only):
#   http://127.0.0.1:8080         (LEADERBOARD_HTTP_PORT)
docker compose logs -f leaderboard     # consume + apply + heartbeat logs
docker compose stop leaderboard        # SIGTERM -> graceful drain -> clean exit 0
```

## Tests

```sh
cd services/leaderboard
go test ./...                                    # unit (no broker, no Node needed)
go test -tags=integration ./test/integration/... # real RabbitMQ via testcontainers (needs Docker)
```

The Go unit/CI build needs **no Node**: `//go:embed all:dist` resolves against the committed
`internal/web/dist/.gitkeep` placeholder, so the SSE/render/consume logic is tested
independently of the frontend. The real Vite bundle is built by the Docker image's node stage.

### Frontend (the trackside board)

```sh
cd services/leaderboard/web
npm install
npm run build      # -> internal/web/dist (the //go:embed target; gitignored except .gitkeep)
npm run dev        # local dev server, proxies /events to a running leaderboard on :8080
```

## Env vars

| Var | Default | Purpose |
|---|---|---|
| `RABBITMQ_HOST` | *(required)* | broker host (compose: `rabbitmq`) |
| `RABBITMQ_PORT` | *(required)* | broker port (`5672`) |
| `RABBITMQ_USER` | *(required)* | broker user |
| `RABBITMQ_PASSWORD` | *(required)* | broker password (never logged) |
| `RABBITMQ_VHOST` | `/` | broker vhost |
| `HTTP_ADDR` | `:8080` | listen addr for the SSE endpoint + embedded SPA |
| `CONSUME_PREFETCH` | `16` | QoS bound on in-flight unacked deliveries (≥ 1) |
| `DLQ_MAX_ATTEMPTS` | `5` | total processing attempts before parking (Story 1.9, Q&A Round 27) |
| `DLQ_RETRY_BASE_MS` | `1000` | first retry-hop delay; hops grow `1s → 2s → 4s → 8s` |
| `DLQ_RETRY_MULTIPLIER` | `2` | exponential backoff growth factor per hop (≥ 1) |
| `DLQ_RETRY_MAX_MS` | `60000` | ceiling on any single retry hop's delay (≥ base) |
| `TIMING_EXCHANGE` | `timing.events` | producer exchange the consumer queue binds to |
| `HEARTBEAT_INTERVAL_MS` | `1000` | heartbeat period |
| `LOG_LEVEL` | `info` | `debug\|info\|warn\|error` |
| `SERVICE_NAME` | `leaderboard` | `source`/`service` identity |
| `LIVENESS_FILE` | `/tmp/pitwall-leaderboard.live` | touch-file the healthcheck reads |
| `CONTRACT_DIR` | *(resolved)* | `/contract` tree (set to `/contract` in the image) |
| `SHUTDOWN_TIMEOUT_MS` | `5000` | bound on graceful drain |
| `DB_PATH` | `/data/leaderboard.db` | private SQLite DB (inbox + standings) on the `leaderboard-data` volume |
| `INSTANCE_ID` | *(minted)* | per-process id; a UUID is generated if unset |

## Layout

```
cmd/leaderboard/main.go       # wiring + graceful shutdown (consumer + heartbeat + SSE server)
internal/config/             # env loading + fail-fast validation
internal/messaging/          # FACADE over libs/go-pitwall/messaging: Leaderboard's own/DLX exchange +
                             #   consumed routing-key constants + domain decoders (the envelope codec,
                             #   validator, consumer Bus + DLQ live in the lib — Story 2.1)
internal/persistence/        # FACADE over libs/go-pitwall/persistence: Leaderboard's migrations (inbox +
                             #   sessions + session-keyed standings) + the read-model store (atomic
                             #   ApplyLap/ApplySessionStarted/ApplySessionEnded; CurrentBoard)
internal/domain/             # pure standings ordering + first-to-set tie-break + short-masterId fallback
internal/consumer/           # validate -> dedupe -> apply (atomic) -> ack/nack; Notify hook to the web layer
internal/web/                # SSE hub + //go:embed SPA + render mapper incl. session status (dist = Vite output)
web/                         # Vite + React + TS source for the leaderboard-row board (UX-DR8)
internal/heartbeat/          # FACADE over libs/go-pitwall/heartbeat (1 s emitter + liveness touch-file)
internal/hygiene/            # source guard test (no bare prints)
# Shared blueprint mechanics (logger, envelope+validator, outbox/inbox, messaging
# runtime, relay, heartbeat, erasure) live ONCE in libs/go-pitwall (replace-pinned).
test/integration/            # testcontainers RabbitMQ end-to-end (Timing -> Leaderboard -> standings -> SSE)
Dockerfile · healthcheck.sh  # multi-stage build (node bundle -> go embed -> alpine; context = repo root); bus-only health
```
