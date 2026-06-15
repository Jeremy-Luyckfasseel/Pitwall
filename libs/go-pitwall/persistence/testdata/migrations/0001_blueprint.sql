-- +goose Up
-- Test-only schema exercising the blueprint outbox + inbox table shapes the library
-- mechanics expect. Each real service ships its own copy of these tables (plus its
-- domain tables) in its own migrations; this file lets the library self-test Open,
-- Migrate, OutboxStore and the inbox helpers without depending on any service.
CREATE TABLE outbox (
    id          TEXT PRIMARY KEY,
    routing_key TEXT NOT NULL,
    payload     BLOB NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'sent', 'quarantined')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    sent_at     TEXT
);
CREATE INDEX idx_outbox_status_created ON outbox (status, created_at);

CREATE TABLE inbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    processed_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE inbox;
DROP TABLE outbox;
