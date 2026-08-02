# Driver

Pitwall's first Python service (Story 3.1) — the SoR for racing identity, full
lap-by-lap history, and the canonical all-time personal record (Epic 3). This story
ships only the **skeleton**: connects to the bus, declares its own `driver.events`
exchange, emits a 1 s `control.heartbeat`, structured JSON logs, and shuts down
gracefully on `SIGTERM`/`SIGINT`. No domain logic yet — racing profile (3.2), lap
history (3.3), PR detection (3.4), and the erasure handler (3.7) build on this skeleton
in later stories.

## Stack

Python 3.14.x, built on [`libs/py-pitwall`](../../libs/py-pitwall/README.md) (the
shared blueprint mechanics) and [`contract/codegen/python`](../../contract/codegen/python)
(the generated wire DTOs). No FastAPI/HTTP surface — bus-only (CLAUDE.md §2; Q&A Round
34/Q34.1). `pika` (sync/blocking) for AMQP; stdlib `sqlite3` for the private store;
Alembic for migrations.

## Local dev

```sh
pip install -e ../../contract/codegen/python
pip install -e ../../libs/py-pitwall
pip install -e .[test]

# Run this service's own migrations (creates the outbox/inbox tables):
DB_PATH=./driver.db python -m alembic upgrade head

# Run the tests:
pytest tests/

# Run the service (needs RabbitMQ — `make up` from the repo root, or point at any broker):
RABBITMQ_HOST=localhost RABBITMQ_USER=pitwall RABBITMQ_PASSWORD=change-me \
  CONTRACT_DIR=../../contract DB_PATH=./driver.db \
  python -m driver.main
```

## Config

See `docker-compose.yml`'s `driver:` block for the full environment variable list
(mirrors Timing's/Identity's shape — `RABBITMQ_*`, `HEARTBEAT_INTERVAL_MS`,
`LOG_LEVEL`, `SERVICE_NAME`, `LIVENESS_FILE`, `CONTRACT_DIR`, `DB_PATH`,
`OUTBOX_POLL_INTERVAL_MS`, `CONSUME_PREFETCH`, `DLQ_*`). `driver.config.load_config`
fails fast (lists every missing required variable) rather than assuming a default for
anything not explicitly optional.

## Deploy status

**Local dev + CI only.** Not yet wired into `docker-compose.prod.yml` or
`release.yml` — same deferral Identity took in Story 2.2 (see
`_bmad-output/implementation-artifacts/deferred-work.md`). Add the prod overlay entry
when Driver is first deployed (a later Epic-3 story).
