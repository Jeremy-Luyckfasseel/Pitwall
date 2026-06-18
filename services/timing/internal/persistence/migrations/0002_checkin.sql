-- +goose Up
-- Story 2.3 (gate check-in): Timing's local transponder->masterId map. As the
-- hardware-facing service, Timing owns this mapping (Q6.3): a transponder (hardware
-- id) handed out at check-in is bound to a driver's canonical masterId, and a gate
-- crossing on that transponder resolves to the masterId locally (no lookup). This
-- story builds the store + the resolution READ path; the hand-out assignment TRIGGER
-- + latest-wins reassignment + logging are Story 2.4 (Q&A Round 32 / Q32.2). An
-- unknown transponder is never minted/guessed here (that operator exception is 2.5).
CREATE TABLE transponder_map (
    transponder_id TEXT PRIMARY KEY,   -- hardware id read at the gate / start-finish line
    master_id      TEXT NOT NULL,      -- canonical lowercase UUID v4 (wire: masterId), issued by Identity
    created_at     TEXT NOT NULL,      -- wire-format timestamp the mapping was first set
    updated_at     TEXT NOT NULL       -- wire-format timestamp of the last (latest-wins) reassignment
);

-- The idempotent inbox (M6, ADR-0005): Timing's FIRST inbound consumer arrives this
-- story (identity.resolved). A row's existence means "this envelope was already
-- processed", so an at-least-once redelivery is a safe no-op. Dedupe is on the
-- envelope id. Matches libs/go-pitwall/persistence/inbox.go.
CREATE TABLE inbox (
    id           TEXT PRIMARY KEY,   -- = envelope id
    type         TEXT NOT NULL,      -- = envelope type (e.g. identity.resolved)
    processed_at TEXT NOT NULL       -- wire-format timestamp when applied
);

-- +goose Down
DROP TABLE inbox;
DROP TABLE transponder_map;
