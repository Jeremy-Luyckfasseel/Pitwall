# Leaderboard

The live trackside standings service (Go) — the walking skeleton's first **consumer**
(Story 1.7). It consumes Timing's `lap.recorded` events off the **`timing.events`**
exchange, dedupes them through a **durable idempotent inbox**, validates every message
against `/contract` **on consume**, folds them into a **best-lap standings** read-model,
and serves a **live trackside board**: a `//go:embed`-ed Vite/React bundle pushed to the
browser over **SSE** (never HTTP polling, ADR-0004). It is a **pure read-model** — it owns
no canonical state (FR41) and is a fold of consumed events.

The inline blueprint blocks here (config, logging, heartbeat, messaging, persistence) are
**duplicated** from `services/timing` by design; they are **extracted** to
`libs/go-pitwall` in **Story 2.1** (grow-don't-pre-scaffold). Session lifecycle/auto-reset
(**1.8**), DLQ/TTL-retry/parking (**1.9**), and the stale flag + replay-from-marker
(**1.10**) build on this.

## What it owns (today)

- Declares and owns the durable **`leaderboard.events`** topic exchange and emits a
  **1 s `control.heartbeat`** to it (bus-only liveness — no HTTP `/health`, ADR-0004),
  validated against `/contract` before publishing.
- A durable queue **`leaderboard.lap-recorded`** bound to the **producer's**
  `timing.events` exchange on routing key `lap.recorded` (a service consumes from the
  producer's exchange and publishes only to its own). Consumed with **manual ack** and a
  bounded QoS prefetch.
- An **idempotent inbox** (private SQLite, `DB_PATH`, on the `leaderboard-data` volume):
  dedupe on the envelope **`id`** (a redelivered lap is a **no-op**, M6). The dedupe-check,
  the projection upsert, and the inbox insert commit in **one transaction**, and the
  message is **acked only after** that commit (NFR6) — the consumer-side mirror of Timing's
  transactional outbox.
- A **standings projection**: one row per driver (`master_id`) holding the driver's current
  **best lap**. Ordering is **best lap ascending**; ties are broken by **whoever set the
  time first** (earlier wire `at`, then ingest sequence for a deterministic, non-flapping
  order) — FR44.
- A **live board over SSE**: a `net/http` server on `HTTP_ADDR` serves the embedded SPA at
  `/` and pushes the standings snapshot to every connected client at `/events` on connect
  and on each read-model change (≤2 s convergence target, M7). This HTTP server is the
  **display, not a health endpoint** (liveness stays the touch-file).
- **Validate-on-consume**: an invalid message (fails `/contract`) is **logged + rejected**
  (`Nack`, no requeue) — never applied to the read-model, never silently dropped. *(There
  is no DLX in 1.7; Story 1.9 adds the `<consumer>.dlx` + TTL-retry + parking so this
  becomes a true dead-letter, and will redeclare the queue with `x-dead-letter-exchange`
  args.)*
- Logs **structured JSON** (one correlationId per process; the per-message correlationId
  threads onto apply/reject logs) and shuts down **gracefully** on SIGTERM/SIGINT.

> **Display names:** Epic 1 has no nicknames or racing numbers, so a row's display name
> falls back to a **short `masterId`**. Each row keys on `masterId` with the display name as
> a separate **overlay** (the `display_name` column, NULL today); a `driver.profile_updated`
> nickname can set it in place in **Epic 3** — 1.7 adds **no** `driver` schema or binding
> (Q&A Round 26, "tolerant of the producer not existing yet").
>
> **Out of scope (deliberately not here):** `session.started`/`session.ended` consumption
> (auto-reset + active/finished status → **1.8**), `sessionId` keying / out-of-order gating
> (**1.8**), DLQ/retry/parking (**1.9**), the stale/reconnecting flag + replay-from-marker
> (**1.10**), and the `libs/go-pitwall` extraction (**2.1**).

## Events

| Direction | Event | Exchange / routing key | Notes |
|---|---|---|---|
| out | `control.heartbeat` | `leaderboard.events` / `control.heartbeat` | cross-cutting liveness; payload `service`, `at`, `instanceId` |
| in  | `lap.recorded` | `timing.events` / `lap.recorded` | validated on consume, deduped on envelope `id`, folded into best-lap standings |

Publishes no domain event (the optional `leaderboard.updated` in the service design is **not**
in 1.7 scope). The live board is served over **SSE**, not the bus.

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
internal/logging/            # the single structured-JSON logger
internal/messaging/          # envelope + consume-decode, validate-on-consume (+ data-schema index),
                             #   own exchange + publisher, the consumer queue (bind to timing.events) + Delivery
internal/persistence/        # SQLite open + pragmas, goose migrations, inbox + standings store (atomic Apply)
internal/domain/             # pure standings ordering + first-to-set tie-break + short-masterId fallback
internal/consumer/           # validate -> dedupe -> apply (atomic) -> ack/nack; Notify hook to the web layer
internal/web/                # SSE hub + //go:embed SPA + render mapper (dist = Vite output)
web/                         # Vite + React + TS source for the leaderboard-row board (UX-DR8)
internal/hygiene/            # source guard test (no bare prints)
test/integration/            # testcontainers RabbitMQ end-to-end (Timing -> Leaderboard -> standings -> SSE)
Dockerfile · healthcheck.sh  # multi-stage build (node bundle -> go embed -> alpine; context = repo root); bus-only health
```
