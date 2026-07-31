package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TransponderStore is Timing's system-of-record for the transponder(hardware-id)->
// masterId mapping (Q6.3): the gate-resolution READ (Resolve), the direct Upsert seam
// used by seeding/tests, and the hand-out assignment TRIGGER with latest-wins
// reassignment reporting (Assign — Story 2.4, Q&A Round 32 / Q32.2).
type TransponderStore struct {
	db *sql.DB
}

// NewTransponderStore binds the store to an already-open, already-migrated database.
func NewTransponderStore(db *sql.DB) *TransponderStore {
	return &TransponderStore{db: db}
}

// execer is satisfied by both *sql.DB and *sql.Tx, letting resolveWith/upsertWith run
// either directly against the store's connection or inside Assign's transaction.
type execer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Resolve returns the masterId bound to transponderID, or ok=false if the hardware id
// is unknown. A false ok is NOT an error and the caller MUST NOT mint an id for it —
// an unknown token at the gate/line is the operator-surfaced exception of Story 2.5,
// never an anonymous identity.
func (s *TransponderStore) Resolve(ctx context.Context, transponderID string) (masterID string, ok bool, err error) {
	return resolveWith(ctx, s.db, transponderID)
}

func resolveWith(ctx context.Context, q execer, transponderID string) (masterID string, ok bool, err error) {
	err = q.QueryRowContext(ctx,
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
//
// The read-then-write runs inside a single transaction (WithinTx pins the store's sole
// SQLite connection for its duration) so two concurrent hand-outs for the same
// transponder can never interleave and report a stale "previous" or silently lose one
// assignment's write.
func (s *TransponderStore) Assign(ctx context.Context, transponderID, masterID, now string) (reassigned bool, previousMasterID string, err error) {
	err = WithinTx(ctx, s.db, func(tx *sql.Tx) error {
		previous, ok, rerr := resolveWith(ctx, tx, transponderID)
		if rerr != nil {
			return fmt.Errorf("assign transponder %q: resolve: %w", transponderID, rerr)
		}
		if uerr := upsertWith(ctx, tx, transponderID, masterID, now); uerr != nil {
			return fmt.Errorf("assign transponder %q: upsert: %w", transponderID, uerr)
		}
		if ok && previous != masterID {
			reassigned, previousMasterID = true, previous
		}
		return nil
	})
	if err != nil {
		return false, "", err
	}
	return reassigned, previousMasterID, nil
}

// Upsert binds transponderID to masterID, latest-wins on re-handout (the hand-out
// trigger that calls this — with reassignment detection + logging — is Assign, Story
// 2.4). created_at is preserved on conflict; updated_at advances to now.
func (s *TransponderStore) Upsert(ctx context.Context, transponderID, masterID, now string) error {
	return upsertWith(ctx, s.db, transponderID, masterID, now)
}

func upsertWith(ctx context.Context, q execer, transponderID, masterID, now string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO transponder_map (transponder_id, master_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(transponder_id) DO UPDATE SET master_id = excluded.master_id, updated_at = excluded.updated_at`,
		transponderID, masterID, now, now)
	if err != nil {
		return fmt.Errorf("upsert transponder %q: %w", transponderID, err)
	}
	return nil
}
