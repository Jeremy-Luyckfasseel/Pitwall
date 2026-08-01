-- +goose Up
-- Story 2.5 (register-first enforcement + the unknown-token operator exception, FR39):
-- a crossing at the start-finish line whose token has no completed check-in this
-- session is never minted, never counted as an anonymous lap, and never dropped — it
-- is HELD here, durably, for a future operator to late-bind. Append-only: every scan
-- attempt (even a repeat of the same stray token) is its own row, so there is no
-- update/delete API and no dedupe. Late-binding RESOLUTION (an operator attaching a
-- held row to a masterId) is a future capability (no operator UI exists yet — Epic 11)
-- and is out of scope here; this migration only builds the durable capture.
CREATE TABLE held_line_scans (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token        TEXT NOT NULL,   -- raw scanned token (transponder hw id, or a masterId-shaped QR read)
    method       TEXT NOT NULL,   -- "qr" | "transponder" (messaging.CheckInMethod* values)
    session_id   TEXT NOT NULL,
    occurred_at  TEXT NOT NULL,   -- wire-format timestamp of the crossing
    reason       TEXT NOT NULL,   -- why it was held, e.g. "no completed check-in this session"
    recorded_at  TEXT NOT NULL    -- wire-format timestamp Timing persisted it
);

-- +goose Down
DROP TABLE held_line_scans;
