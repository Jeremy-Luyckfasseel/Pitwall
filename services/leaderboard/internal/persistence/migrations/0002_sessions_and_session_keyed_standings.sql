-- +goose Up
-- Story 1.8: the read-model becomes SESSION-AWARE (FR43/FR45, NFR24).
--
-- sessions: one row per session ever seen.
--   epoch      = LOCALLY-assigned monotonic order-of-first-sight (NOT a wire
--                field — the session schemas carry no epoch). The current board
--                is the row with MAX(epoch); "auto-reset" is this pointer
--                moving, never a DELETE (a replayed start can't wipe anything).
--   status     = forward-only gate: implicit -> active -> finished.
--                'implicit' = laps seen before the session.started (the NFR24
--                implicit board); a late start reconciles it to 'active'; an
--                end is terminal ('finished' never reopens).
--   started_at / ended_at = wire timestamps, filled in whenever their event
--                arrives (NULL until then).
CREATE TABLE sessions (
    session_id TEXT PRIMARY KEY,
    epoch      INTEGER NOT NULL UNIQUE,
    status     TEXT NOT NULL CHECK (status IN ('implicit','active','finished')),
    started_at TEXT,
    ended_at   TEXT
);

-- standings re-keyed per session: the same driver holds an INDEPENDENT best in
-- every session (the board resets because the new session simply has no rows
-- yet). SQLite cannot alter a primary key, so the table is recreated. The 1.7
-- rows carry no session provenance and cannot be backfilled — they are dropped.
-- That is safe and honest: the projection is a rebuildable pure fold (FR41) and
-- only dev data exists before the first deploy (Story 1.12).
DROP TABLE standings;
CREATE TABLE standings (
    session_id    TEXT NOT NULL,
    master_id     TEXT NOT NULL,
    best_lap_ms   INTEGER NOT NULL,
    best_lap_at   TEXT NOT NULL,
    best_lap_seq  INTEGER NOT NULL,
    display_name  TEXT,
    PRIMARY KEY (session_id, master_id)
);

-- Read-side ordering scan per session: best lap asc, earliest-set, ingest seq.
CREATE INDEX idx_standings_order ON standings (session_id, best_lap_ms, best_lap_at, best_lap_seq);

-- +goose Down
DROP TABLE standings;
CREATE TABLE standings (
    master_id     TEXT PRIMARY KEY,
    best_lap_ms   INTEGER NOT NULL,
    best_lap_at   TEXT NOT NULL,
    best_lap_seq  INTEGER NOT NULL,
    display_name  TEXT
);
CREATE INDEX idx_standings_order ON standings (best_lap_ms, best_lap_at, best_lap_seq);
DROP TABLE sessions;
