-- +goose Up
-- Story 3.5 (scanner-offline — persist-first, never a faked lap, FR38 / Q6.9): Timing's
-- durable "flag the gap" record. When the start-finish scanner goes silent mid-session,
-- prior laps are already persisted (persist-first) and never lost (M3); the physically-
-- missed crossings are acknowledged as a GAP and never faked into laps. This table is the
-- local, operator-queryable record of each outage (Control Room / Epic 12 reads it later),
-- mirroring held_line_scans' "hold + flag, never dropped" posture (Q38.2).
--
-- One row per outage: OpenOutage inserts it (online_at NULL = still open) when the scanner
-- goes offline; CloseOutage sets online_at on recovery. gap_from is the wire time of the
-- last good crossing before the gap (= since when none yet this session); since is when the
-- scanner was detected offline. No delete API — retention is Story 3.7 / Epic 14.
CREATE TABLE scanner_outages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    scanner_id   TEXT NOT NULL,   -- the silent scanner ("start-finish" today)
    session_id   TEXT NOT NULL,
    gap_from     TEXT NOT NULL,   -- wire time of the last good crossing before the gap (= since if none)
    since        TEXT NOT NULL,   -- wire time the scanner was detected offline (outage start)
    online_at    TEXT,            -- wire time of recovery; NULL while the outage is still open
    recorded_at  TEXT NOT NULL    -- wire time Timing persisted the OpenOutage row
);

-- +goose Down
DROP TABLE scanner_outages;
