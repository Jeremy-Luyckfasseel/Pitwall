-- +goose Up
-- Story 3.4 (canonical PR & live PR detection, FR37): Timing's LOCAL copy of each
-- driver's all-time personal record — an ECST cache, NOT the system of record (Driver
-- owns the canonical PR). One row per driver (master_id PK). It is written two ways:
--   1. ObserveLap advances it optimistically the instant a counted lap beats it
--      (Q37.3), so the next lap compares against the new best and Timing emits one
--      personal_record.broken per genuine new best.
--   2. Refresh overwrites it with Driver's confirmed canonical value on a consumed
--      driver.pr_updated (latest-confirmed-wins).
-- best_lap_ms is the fastest lap in ms. session_id / set_at record which lap set it
-- (set_at = that lap's wire time); both are nullable only defensively (a Refresh seeds
-- them from the event). updated_at is the wire time of the last write.
CREATE TABLE driver_prs (
    master_id    TEXT PRIMARY KEY,
    best_lap_ms  INTEGER NOT NULL,
    session_id   TEXT,
    set_at       TEXT,
    updated_at   TEXT NOT NULL
);

-- +goose Down
DROP TABLE driver_prs;
