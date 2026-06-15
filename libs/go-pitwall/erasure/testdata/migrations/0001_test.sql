-- +goose Up
-- Test-only schema: a stand-in PII slice (things), the idempotent inbox the erasure
-- scaffold dedupes on, and a tombstones table the guard/writer use. A real service
-- supplies its own equivalents in its own migrations.
CREATE TABLE things (
    master_id TEXT PRIMARY KEY,
    payload   TEXT NOT NULL
);
CREATE TABLE inbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    processed_at TEXT NOT NULL
);
CREATE TABLE tombstones (
    master_id TEXT PRIMARY KEY
);

-- +goose Down
DROP TABLE tombstones;
DROP TABLE inbox;
DROP TABLE things;
