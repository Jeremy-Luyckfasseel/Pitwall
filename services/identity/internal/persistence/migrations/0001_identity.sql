-- +goose Up
-- Identity's system-of-record (Story 2.2, FR1/FR3/NFR15): exactly ONE canonical
-- masterId per person, de-duplicated on email. Identity stores ONLY masterId + email
-- + status + timestamps -- never passwords, profile, or racing data, and no fuzzy
-- merging. The UNIQUE(email) constraint is the single-writer race guarantee: two
-- concurrent lookups for the same NEW email -> the first INSERT wins, the second hits
-- the constraint and re-selects the winner's id (one mint, never two).
CREATE TABLE identities (
    master_id  TEXT PRIMARY KEY,                 -- canonical lowercase UUID v4 (wire: masterId)
    email      TEXT NOT NULL UNIQUE,             -- the natural key Identity de-duplicates on
    status     TEXT NOT NULL DEFAULT 'active'
               CHECK (status IN ('active')),     -- room to grow (e.g. tombstoned in Story 2.6)
    created_at TEXT NOT NULL,                     -- wire-format timestamp the id was minted
    updated_at TEXT NOT NULL                      -- wire-format timestamp of the last change
);

-- The idempotent inbox (M6, ADR-0005): a row's existence means "this envelope was
-- already processed", so a redelivered identity.lookup_requested is a safe no-op
-- (AC3). Dedupe is on the envelope id. Matches libs/go-pitwall/persistence/inbox.go.
CREATE TABLE inbox (
    id           TEXT PRIMARY KEY,   -- = envelope id
    type         TEXT NOT NULL,      -- = envelope type (identity.lookup_requested)
    processed_at TEXT NOT NULL       -- wire-format timestamp when applied
);

-- The transactional outbox (Story 1.4 mechanics, libs/go-pitwall/persistence/outbox.go):
-- the identity.resolved reply is committed in the SAME tx as the mint/resolve, then the
-- relay publishes it to identity.events and marks it sent only after a broker ack -- so
-- the reply survives a bus outage (the sad-path "RabbitMQ down -> replies via outbox").
CREATE TABLE outbox (
    id          TEXT PRIMARY KEY,                       -- = envelope id (UUID v7, time-ordered)
    routing_key TEXT NOT NULL,                          -- = envelope type; published key on identity.events
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
DROP TABLE inbox;
DROP TABLE identities;
