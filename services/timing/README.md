# Timing

The lap-timing service (Go). This directory currently holds the **Go service
skeleton on the bus** (Story 1.3) — the inline blueprint blocks every Pitwall Go
service is built from. Timing's domain (laps, sessions, the simulator) is added in
Stories 1.5–1.8; the blueprint machinery here is **extracted** to `libs/go-pitwall`
in Epic 2 (grow-don't-pre-scaffold).

## What it owns (today)

- Declares and owns the durable **`timing.events`** topic exchange.
- Emits a **1 s `control.heartbeat`** to that exchange (bus-only liveness — no HTTP
  `/health`, ADR-0004). Each heartbeat is **validated against `/contract` before
  publishing**; an invalid one is logged + dropped, never published.
- Maintains a **liveness touch-file** the Docker `healthcheck` reads.
- Logs **structured JSON** (one correlationId per process lifecycle) and shuts down
  **gracefully** on SIGTERM/SIGINT.

## Events

| Direction | Event | Exchange / routing key | Notes |
|---|---|---|---|
| out | `control.heartbeat` | `timing.events` / `control.heartbeat` | cross-cutting liveness; payload `service`, `at`, `instanceId` (Q&A Round 25) |

Consumes nothing yet (the idempotent inbox + lap/session events arrive in later
Epic-1 stories).

## Run

```sh
# whole stack (from repo root): brings up RabbitMQ then Timing, waits for health
make up            # or: docker compose up -d

docker compose logs -f timing          # watch the structured JSON heartbeat lifecycle
docker compose stop timing             # SIGTERM -> graceful drain -> clean exit 0
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
| `SHUTDOWN_TIMEOUT_MS` | `5000` | bound on graceful drain |
| `INSTANCE_ID` | *(minted)* | per-process id; a UUID is generated if unset |

## Layout

```
cmd/timing/main.go            # wiring + graceful shutdown (+ outbox-flush seam for Story 1.4)
internal/config/             # env loading + fail-fast validation
internal/logging/            # the single structured-JSON logger
internal/messaging/          # envelope, validate-on-publish, own exchange + publisher
internal/heartbeat/          # 1 s emitter + liveness touch-file
internal/hygiene/            # source guard test (no bare prints)
test/integration/            # testcontainers RabbitMQ end-to-end
Dockerfile · healthcheck.sh  # multi-stage build (context = repo root); bus-only health
```
