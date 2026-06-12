# Timing

The lap-timing service (Go). This directory holds the **Go service skeleton on the
bus** (Story 1.3), the **transactional outbox + relay reliability spine**
(Story 1.4), the **development simulator** (Story 1.5), and the **lap-validity
filter** (Story 1.6) — the inline blueprint blocks every Pitwall Go service is built
from. The remaining Timing domain (session lifecycle/out-of-order tolerance, scanner
offline, PR detection) arrives in Stories 1.7–1.8 + Epic 3; the blueprint machinery
here is **extracted** to `libs/go-pitwall` in Epic 2 (grow-don't-pre-scaffold).

## What it owns (today)

- Declares and owns the durable **`timing.events`** topic exchange.
- Emits a **1 s `control.heartbeat`** to that exchange (bus-only liveness — no HTTP
  `/health`, ADR-0004). Each heartbeat is **validated against `/contract` before
  publishing**; an invalid one is logged + dropped, never published. The heartbeat
  is **ephemeral** — it is **not** outbox-backed (never replay a stale beat).
- A private **SQLite** database (`DB_PATH`, on the `timing-data` volume) holding the
  **transactional outbox**.
- A background **relay** (Story 1.4): a producer writes domain state **and** an
  outbox row in **one local transaction**; the relay then publishes each row to
  `timing.events`, **validating against `/contract` first** and marking the row
  **`sent` only after a broker publisher-confirm ack**. Row lifecycle:
  - `pending → sent` — confirmed ack.
  - `pending → quarantined` — failed `/contract` validation (terminal; never
    published, never retried — a **producer-side quarantine**, distinct from the
    consumer-side RabbitMQ DLQ/parking topology in Story 1.9).
  - stays `pending` — broker unreachable; retried (capped backoff) **forever** with
    **no loss**, surviving a bus outage **and** a service restart.
- Maintains a **liveness touch-file** the Docker `healthcheck` reads.
- Logs **structured JSON** (one correlationId per process lifecycle) and shuts down
  **gracefully** on SIGTERM/SIGINT, with a bounded best-effort **outbox flush**.
- An **env-toggled simulator** (Story 1.5 — `SIMULATOR_ENABLED`, **off** in code,
  **on** in the local compose demo). When on it runs **continuous sessions** for
  **N fixture drivers**, streaming `session.started → lap.recorded → session.ended`
  through the outbox so the platform runs end-to-end with **no hardware** (FR40).
  Each driver's first crossing is the **out-lap (start marker, uncounted)**; each
  subsequent crossing emits a lap with `lapTimeMs` = delta from the previous
  crossing. Lap times are drawn from a configurable normal distribution; a
  `SIM_SEED` makes a session reproducible.
- **Lap-validity filter** (Story 1.6 — `MIN_LAP_TIME_MS`, FR35). The pure
  per-driver crossing→lap rule (`internal/domain`) rejects a crossing closer than
  `MIN_LAP_TIME_MS` to the driver's previous **valid** crossing as a
  **bounce/duplicate** read: it is ignored (no `lap.recorded`) and does **not**
  advance the driver's baseline, so the next real lap is still timed from the last
  valid crossing. The first crossing stays the uncounted start marker, and validity
  is tracked **per driver**. The simulator keeps `SIM_LAP_MEAN_MS` well above
  `MIN_LAP_TIME_MS`, so on clean simulated input the filter rejects nothing.

> **Fixture masterIds are NOT an identity path.** The simulator mints N valid-format
> UUID-v4 ids locally for its drivers; real Identity-issued ids replace them in
> Epic 2. No id-minting path is baked into the skeleton.
>
> The producer seam is `relay.NewEnqueuer(db, store, validate, relay)` (commits the
> outbox row in its own tx, then kicks the relay). The **consumer-side session gating /
> out-of-order tolerance** (Story 1.8) lives in `services/leaderboard`; the **event
> store + replay** (ADR-0005, Story 1.10) is deliberately not here.

## Events

| Direction | Event | Exchange / routing key | Notes |
|---|---|---|---|
| out | `control.heartbeat` | `timing.events` / `control.heartbeat` | cross-cutting liveness; payload `service`, `at`, `instanceId` (Q&A Round 25) |
| out | `session.started` | `timing.events` / `session.started` | ACTUAL session start (simulator-generated); operator-driven path is Epic 11 |
| out | `lap.recorded` | `timing.events` / `lap.recorded` | one per counted lap; `lapTimeMs` = delta from previous valid crossing |
| out | `session.ended` | `timing.events` / `session.ended` | carries a minimal per-driver `summary` (tolerant/unpinned v1) |

Consumes nothing yet (the idempotent inbox arrives in later Epic-1 stories).

## Run

```sh
# whole stack (from repo root): brings up RabbitMQ then Timing, waits for health
make up            # or: docker compose up -d

# Simulator is ON by default in compose — watch a live lap stream:
docker compose logs -f timing          # session.started -> lap.recorded... -> session.ended (+ heartbeats)
docker compose stop timing             # SIGTERM -> graceful drain -> clean exit 0

# Heartbeat-only (no simulator): set SIMULATOR_ENABLED=false in .env.
```

## Tests

```sh
cd services/timing
go test ./...                                   # unit (no broker needed)
go test -tags=integration ./test/integration/...# real RabbitMQ via testcontainers (needs Docker)
```

> No Go toolchain locally? Build/test in the pinned image:
> `docker run --rm -v "$PWD/../..":/app -w /app/services/timing golang:1.26.4 go test ./...`

## Env vars

| Var | Default | Purpose |
|---|---|---|
| `RABBITMQ_HOST` | *(required)* | broker host (compose: `rabbitmq`) |
| `RABBITMQ_PORT` | *(required)* | broker port (`5672`) |
| `RABBITMQ_USER` | *(required)* | broker user |
| `RABBITMQ_PASSWORD` | *(required)* | broker password (never logged) |
| `RABBITMQ_VHOST` | `/` | broker vhost |
| `HEARTBEAT_INTERVAL_MS` | `1000` | heartbeat period |
| `LOG_LEVEL` | `info` | `debug\|info\|warn\|error` |
| `SERVICE_NAME` | `timing` | `source`/`service` identity |
| `LIVENESS_FILE` | `/tmp/pitwall-timing.live` | touch-file the healthcheck reads |
| `CONTRACT_DIR` | *(resolved)* | `/contract` tree (set to `/contract` in the image) |
| `HEALTH_FRESHNESS_FACTOR` | `3` | healthcheck tolerates N missed beats |
| `SHUTDOWN_TIMEOUT_MS` | `5000` | bound on graceful drain (+ outbox flush) |
| `DB_PATH` | `/data/timing.db` | private SQLite DB (on the `timing-data` volume) |
| `OUTBOX_POLL_INTERVAL_MS` | `200` | relay poll backstop (enqueue also kicks the relay) |
| `INSTANCE_ID` | *(minted)* | per-process id; a UUID is generated if unset |
| `SIMULATOR_ENABLED` | `false` (code) / `true` (compose) | toggle the simulator; `true\|false\|1\|0` |
| `SIM_DRIVERS` | *(required when on)* | N fixture drivers (≥ 1) |
| `SIM_LAP_MEAN_MS` | *(required when on)* | normal-distribution mean lap time (ms, ≥ 1) |
| `SIM_LAP_STDDEV_MS` | *(required when on)* | normal-distribution stddev (ms, ≥ 0) |
| `SIM_SESSION_LAPS` | *(required when on)* | counted laps per driver before the session ends (≥ 1) |
| `MIN_LAP_TIME_MS` | *(required when on)* | lap-validity rule (FR35): crossings closer than this are rejected as bounce/duplicate (≥ 1; must be **below** `SIM_LAP_MEAN_MS`) |
| `SIM_TICK_MS` | `250` | wall-clock pacing between emitted events |
| `SIM_SESSION_GAP_MS` | `5000` | pause between sessions in the continuous loop |
| `SIM_SEED` | *(time-seeded)* | optional integer for a reproducible session |

When `SIMULATOR_ENABLED` is on, the five **required** knobs above must be set or the
service **fails fast** at startup naming each missing/invalid one (golden rule — never
assumed); it also fails fast if `MIN_LAP_TIME_MS >= SIM_LAP_MEAN_MS` (which would reject
most laps). The pacing knobs are correctness-neutral and default. `MIN_LAP_TIME_MS` is
the lap-validity rule for every crossing, not strictly a simulator knob — it is required
when the simulator is on because that is the only crossing source today (Epic 1).

## Layout

```
cmd/timing/main.go            # wiring + graceful shutdown + real outbox flush + simulator goroutine
internal/config/             # env loading + fail-fast validation (incl. simulator knobs)
internal/logging/            # the single structured-JSON logger
internal/messaging/          # envelope + domain-event builders, validate-on-publish (+ data-schema
                             #   index), exchange, publisher + confirm-mode channel
internal/persistence/        # SQLite open + pragmas, goose migrations, the outbox store
internal/relay/              # the outbox relay loop + EnqueueEnvelope / NewEnqueuer producer seam
internal/domain/             # pure crossing -> lap rule (start marker, per-driver delta/lapNumber, min-lap filter)
internal/simulator/          # the env-toggled simulator: drivers, distribution, session lifecycle
internal/heartbeat/          # 1 s emitter + liveness touch-file
internal/hygiene/            # source guard test (no bare prints)
test/integration/            # testcontainers RabbitMQ end-to-end (heartbeat + outbox + simulator stream)
Dockerfile · healthcheck.sh  # multi-stage build (context = repo root); bus-only health
```
