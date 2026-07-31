package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TransponderStore is Timing's system-of-record for the transponder(hardware-id)->
// masterId mapping (Q6.3). This story (2.3) owns the gate-resolution READ (Resolve)
// plus a direct Upsert seam used by seeding/tests; the hand-out assignment TRIGGER +
// latest-wins reassignment logging is wired in Story 2.4 (Q&A Round 32 / Q32.2).
type TransponderStore struct {
	db *sql.DB
}

// NewTransponderStore binds the store to an already-open, already-migrated database.
func NewTransponderStore(db *sql.DB) *TransponderStore {
	return &TransponderStore{db: db}
}

// Resolve returns the masterId bound to transponderID, or ok=false if the hardware id
// is unknown. A false ok is NOT an error and the caller MUST NOT mint an id for it —
// an unknown token at the gate/line is the operator-surfaced exception of Story 2.5,
// never an anonymous identity.
func (s *TransponderStore) Resolve(ctx context.Context, transponderID string) (masterID string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT master_id FROM transponder_map WHERE transponder_id = ?`, transponderID).Scan(&masterID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve transponder %q: %w", transponderID, err)
	}
	return masterID, true, nil
}

// Assign is the hand-out trigger (Story 2.4, FR33): it binds transponderID to masterID
// and reports whether this hand-out CHANGED an existing mapping to a different driver.
// reassigned=false covers both a first-time hand-out (no prior mapping) and an
// idempotent replay of the same hand-out (previous == masterID) — callers should log a
// reassignment only when reassigned=true, never a spurious one on a safe replay.
func (s *TransponderStore) Assign(ctx context.Context, transponderID, masterID, now string) (reassigned bool, previousMasterID string, err error) {
	previous, ok, err := s.Resolve(ctx, transponderID)
	if err != nil {
		return false, "", fmt.Errorf("assign transponder %q: %w", transponderID, err)
	}
	if err := s.Upsert(ctx, transponderID, masterID, now); err != nil {
		return false, "", err
	}
	if ok && previous != masterID {
		return true, previous, nil
	}
	return false, "", nil
}

// Upsert binds transponderID to masterID, latest-wins on re-handout (the store supports
// reassignment; the hand-out trigger that calls this lands in Story 2.4). created_at is
// preserved on conflict; updated_at advances to now.
func (s *TransponderStore) Upsert(ctx context.Context, transponderID, masterID, now string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO transponder_map (transponder_id, master_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(transponder_id) DO UPDATE SET master_id = excluded.master_id, updated_at = excluded.updated_at`,
		transponderID, masterID, now, now)
	if err != nil {
		return fmt.Errorf("upsert transponder %q: %w", transponderID, err)
	}
	return nil
}
