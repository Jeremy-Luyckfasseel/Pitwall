# Changelog — `libs/py-pitwall`

## v0.1.0 (Story 3.1)

Initial release: structured logger, envelope codec, `/contract` validator,
persistence (SQLite/Alembic + outbox/inbox), `pika`-based messaging runtime
(supervisor/publisher/consumer + DLQ/TTL-retry/parking), heartbeat emitter,
erasure-handler scaffold + tombstone guard. Mirrors `libs/go-pitwall` v0.2.0's
mechanics, built fresh (no Python duplication existed to extract from — this is
the first Python service).
