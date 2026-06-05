-- +goose Up
-- The transactional outbox (Story 1.4): a durable row is committed in the SAME
-- local transaction as the producing state change, then a relay publishes it to
-- the service's own exchange and marks it sent only after a broker ack.
--   status: pending -> sent (confirmed ack) | pending -> quarantined (failed
--   /contract validation; terminal, never published, never retried -- a
--   PRODUCER-SIDE quarantine, distinct from the consumer-side RabbitMQ DLQ).
--   A broker-unreachable row stays pending and is retried indefinitely (our own
--   valid events are never poison -- no parking).
CREATE TABLE outbox (
    id          TEXT PRIMARY KEY,                       -- = envelope id (UUID v7, time-ordered)
    routing_key TEXT NOT NULL,                          -- = envelope type; published key on timing.events
    payload     BLOB NOT NULL,                          -- the full, already-marshalled envelope JSON
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'sent', 'quarantined')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,                          -- wire-format timestamp; relay publishes oldest-first
    sent_at     TEXT
);

-- The relay scans pending rows oldest-first; this index keeps that scan cheap.
CREATE INDEX idx_outbox_status_created ON outbox (status, created_at);

-- +goose Down
DROP TABLE outbox;
