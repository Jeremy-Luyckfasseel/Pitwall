# Driver

Pitwall's first Python service (Story 3.1) — the SoR for racing identity, full
lap-by-lap history, and the canonical all-time personal record (Epic 3). Story 3.1
shipped the **skeleton**: connects to the bus, declares its own `driver.events`
exchange, emits a 1 s `control.heartbeat`, structured JSON logs, and shuts down
gracefully on `SIGTERM`/`SIGINT`.

**Story 3.2 adds Driver's first domain logic: the racing-profile safety net.**
Driver consumes `lap.recorded` (`timing.events`) and `identity.resolved`
(`identity.events`) off one durable queue (`driver.profile-safety-net`, its own DLQ
topology on `driver.dlx`, its own dedicated AMQP connection — separate from the one
heartbeat/relay publish on). On first sight of a `masterId` it idempotently creates a
minimal `driver_profiles` row (`racing_number`/`kart_class`/`nickname` all null) and
publishes `driver.profile_updated` (FR12). The creation is permanent: every later
trigger for the same `masterId` is a structural no-op — `driver.persistence.profiles
.insert_minimal_profile` can only ever `INSERT ... WHERE NOT EXISTS`, so a second
write can never overwrite the first (FR13, "Driver's write wins"), and every skip is
logged. PR detection (3.4) and the erasure handler (3.7) build on this in later stories.

**Story 3.3 adds lap-by-lap history and per-session summaries.** The same consumer now
also handles `session.ended` (bound on the existing `timing.events` binding) and does
more with each event:

- On `lap.recorded`, in the same transaction as the safety net, it appends the lap to
  `driver_laps` (`INSERT ... ON CONFLICT DO NOTHING`; a `(master_id, session_id,
  lap_number)` PK plus a `UNIQUE source_event_id` index make a redelivered lap a no-op —
  the inbox is the primary idempotency guard, these are belt-and-braces). FR9/NFR3.
- On `session.ended`, for **each** row of `summary[]` it runs the safety net for that
  row's `masterId` (so an unknown driver is created, never dropped — Q&A Round 36/Q36.3),
  stores the per-`(master_id, session_id)` result in `driver_session_summaries`, and
  publishes **one** `driver.history_appended {masterId, sessionId, bestLapMs, lapCount}`
  per row (Q36.2). A single inbox mark covers the whole envelope, so a redelivered
  `session.ended` publishes no duplicate history and stores no duplicate rows. FR10.

The `session.ended.summary[]` item shape was pinned at this consume point (Q36.1):
`masterId` required, `bestLapMs`/`lapCount` optional, item still tolerant.

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

# Run this service's own migrations (outbox/inbox + driver_profiles + driver_laps/driver_session_summaries):
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
`OUTBOX_POLL_INTERVAL_MS`, `CONSUME_PREFETCH`, `DLQ_*`, and — Story 3.2 —
`TIMING_EXCHANGE`/`IDENTITY_EXCHANGE`, the producer exchanges the profile
safety-net's queue binds to). `driver.config.load_config` fails fast (lists every
missing required variable) rather than assuming a default for anything not explicitly
optional.

## Deploy status

**Local dev + CI only.** Not yet wired into `docker-compose.prod.yml` or
`release.yml` — same deferral Identity took in Story 2.2 (see
`_bmad-output/implementation-artifacts/deferred-work.md`). Add the prod overlay entry
when Driver is first deployed (a later Epic-3 story).
