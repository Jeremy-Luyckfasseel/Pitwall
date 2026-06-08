-- +goose Up
-- The idempotent inbox (M6, ADR-0005): a row's existence means "this envelope was
-- already processed", so a redelivered message is a no-op. Dedupe is on the
-- envelope id (a time-ordered UUID v7 -> cheap PK index). No status column is
-- needed in Story 1.7; the DLQ/retry/parking lifecycle is Story 1.9.
CREATE TABLE inbox (
    id           TEXT PRIMARY KEY,   -- = envelope id
    type         TEXT NOT NULL,      -- = envelope type (e.g. lap.recorded)
    processed_at TEXT NOT NULL       -- wire-format timestamp when applied
);

-- The standings projection: one row per driver, the driver's CURRENT best lap.
-- A pure fold of consumed lap.recorded events (FR41) -- owns no canonical state.
--   best_lap_at  = wire `at` of the lap that set the current best = the FIRST
--                  time that time was achieved (the FR44 tie-break key).
--   best_lap_seq = monotonic ingest order; the deterministic final tie-break
--                  when two bests share an identical best_lap_at (no flapping).
--   display_name = nullable nickname OVERLAY (Q26.3). NULL in Epic 1 -> render
--                  falls back to a short master_id; a driver.profile_updated
--                  nickname can set it in place in Epic 3 without touching the
--                  row identity or its lap data.
CREATE TABLE standings (
    master_id     TEXT PRIMARY KEY,
    best_lap_ms   INTEGER NOT NULL,
    best_lap_at   TEXT NOT NULL,
    best_lap_seq  INTEGER NOT NULL,
    display_name  TEXT
);

-- Read-side ordering scan: best lap ascending, then earliest-set, then ingest seq.
CREATE INDEX idx_standings_order ON standings (best_lap_ms, best_lap_at, best_lap_seq);

-- +goose Down
DROP TABLE standings;
DROP TABLE inbox;
