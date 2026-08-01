-- +goose Up
-- Story 2.6 (DG-3/DG-7): erasure's durable footprint. Three tables, each a narrow,
-- single-purpose write target — no reuse of `identities.status` (that column's original
-- "room to grow (e.g. tombstoned)" comment predates the real erasure design landing in
-- libs/go-pitwall/erasure: a SEPARATE small tombstone table, not a status flag, is the
-- actual established pattern — see that package's own test schema).

-- The masterId-keyed tombstone: the durable "this identity was erased here" audit record
-- (AC1's literal "records a tombstone"). Write-only from this story's own code paths —
-- matches Story 2.5's held_line_scans precedent of building the durable capture a future
-- capability will read, not pre-building the reader.
CREATE TABLE identity_tombstones (
    master_id     TEXT PRIMARY KEY,
    tombstoned_at TEXT NOT NULL
);

-- The email-hash suppression list (Round 33/Q33.1): an IRREVERSIBLE SHA-256 hash of the
-- normalized email (never the plaintext — the plaintext is what got deleted). Lets a
-- later lookup for the same address be recognized and held without retaining the PII
-- erasure was supposed to remove.
CREATE TABLE email_suppressions (
    email_hash    TEXT PRIMARY KEY,
    tombstoned_at TEXT NOT NULL
);

-- The "held, never minted, never dropped" sink for a lookup that hit a suppressed email
-- (AC2) — append-only, same shape as Story 2.5's held_line_scans. No plaintext email:
-- only the hash, so this audit trail cannot itself reintroduce the erased PII.
CREATE TABLE held_lookups (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id   TEXT NOT NULL,
    email_hash   TEXT NOT NULL,
    reason       TEXT NOT NULL,
    occurred_at  TEXT NOT NULL,
    recorded_at  TEXT NOT NULL
);

-- +goose Down
DROP TABLE held_lookups;
DROP TABLE email_suppressions;
DROP TABLE identity_tombstones;
