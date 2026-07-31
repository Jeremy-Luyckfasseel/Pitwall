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
- **Live bus-kill reconnection** (Story 1.10): the Publisher is **reconnect-aware**.
  amqp091-go does **not** auto-recover a dropped connection, so a built-in supervisor
  re-dials with capped-exponential backoff (500 ms → 5 s ceiling) and re-declares the
  exchange + confirm channel on a **mid-session** RabbitMQ kill (not just a restart). The
  relay's and heartbeat's publish calls transparently use the **current** channel under a
  mutex, so on restore the outbox **flushes automatically** with no loss and no duplicate
  beyond what the consumer's inbox dedupes (NFR2). While down, the heartbeat publish fails
  and is logged+skipped, leaving the liveness touch-file stale — the honest bus-down signal.
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

> **Register-first (Story 2.3): the simulator resolves REAL ids via Identity.** It no
> longer mints fixtures — before a driver goes on track it publishes
> `identity.lookup_requested` (to `frontend.events`, impersonating the registration
> producer) and consumes `identity.resolved`, then uses that canonical `masterId` for
> `driver.checked_in` and every `lap.recorded`. Timing thus becomes **dual-role** (its
> first inbound consumer); a `Resolver` (`internal/consumer`) retries the lookup until
> Identity replies, so a cold start or Identity restart recovers rather than hangs.
>
> **Gate check-in.** Each session now opens with one `driver.checked_in` per driver at
> the entry gate: QR drivers carry the `masterId` directly (`checkInMethod:"qr"`);
> transponder drivers (`SIM_TRANSPONDERS`) carry a hardware id resolved via Timing's
> local **`transponder_map`** (`checkInMethod:"transponder"`). `TransponderStore.Assign`
> is the **hand-out trigger** (Story 2.4, FR33): it binds a transponder to a
> register-first-resolved `masterId`, latest-wins on reassignment, and reports the
> change so the caller can log it (the simulator's `Prepare` logs a plain hand-out at
> info, a reassignment at warn with the previous `masterId`). Entirely Timing-internal —
> no new bus event (Q&A Round 6/Q6.3, Round 32).
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
| out | `driver.checked_in` | `timing.events` / `driver.checked_in` | gate check-in (Story 2.3); `masterId`, `at`, `checkInMethod` (`qr`\|`transponder`), nullable `transponderId` |
| out | `lap.recorded` | `timing.events` / `lap.recorded` | one per counted lap; `lapTimeMs` = delta from previous valid crossing |
| out | `session.ended` | `timing.events` / `session.ended` | carries a minimal per-driver `summary` (tolerant/unpinned v1) |
| out | `identity.lookup_requested` | `frontend.events` / `identity.lookup_requested` | register-first lookup (Story 2.3); simulator impersonates the Frontend producer (`source:"frontend"`) |
| in | `identity.resolved` | `identity.events` / `identity.resolved` | Identity's reply; idempotent inbox + DLQ/retry/parking; signals the waiting register-first lookup |

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
| `SIM_TRANSPONDERS` | `0` | how many sim drivers check in via a transponder (rest QR); `0..SIM_DRIVERS` (Story 2.3) |
| `CONSUME_PREFETCH` | `16` | QoS for the `identity.resolved` consumer (> 0) |
| `DLQ_MAX_ATTEMPTS` | `5` | consumer DLQ: processing attempts before parking (Q&A Round 27) |
| `DLQ_RETRY_BASE_MS` | `1000` | consumer DLQ: first retry-hop backoff (ms) |
| `DLQ_RETRY_MULTIPLIER` | `2` | consumer DLQ: exponential backoff factor |
| `DLQ_RETRY_MAX_MS` | `60000` | consumer DLQ: per-hop backoff ceiling (ms, ≥ base) |

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
internal/messaging/          # FACADE over libs/go-pitwall/messaging: Timing's exchange + routing-key
                             #   constants + domain-event builders/types (the envelope codec,
                             #   validator, publisher live in the lib — Story 2.1)
internal/persistence/        # FACADE over libs/go-pitwall/persistence: Timing's migrations (outbox
                             #   table DDL) + Open wiring (db/outbox mechanics live in the lib)
internal/relay/              # FACADE over libs/go-pitwall/relay (outbox relay + producer seam)
internal/domain/             # pure crossing -> lap rule (start marker, per-driver delta/lapNumber, min-lap filter)
internal/consumer/           # identity.resolved consumer (Handler + DLQ) + the register-first Resolver bridge (Story 2.3)
internal/simulator/          # the env-toggled simulator: register-first resolve, gate check-ins, distribution, session lifecycle
internal/heartbeat/          # FACADE over libs/go-pitwall/heartbeat (1 s emitter + liveness touch-file)
internal/hygiene/            # source guard test (no bare prints)
# Shared blueprint mechanics (logger, envelope+validator, outbox/inbox, messaging
# runtime, relay, heartbeat, erasure) live ONCE in libs/go-pitwall (replace-pinned).
test/integration/            # testcontainers RabbitMQ end-to-end (heartbeat + outbox + simulator stream)
Dockerfile · healthcheck.sh  # multi-stage build (context = repo root); bus-only health
```
