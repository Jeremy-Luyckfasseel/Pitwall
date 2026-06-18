package persistence

import (
	"context"
	"database/sql"
	"fmt"
)

// Store is Identity's system-of-record over the identities table. It owns the one
// canonical masterId per email (FR1/FR3/NFR15) and nothing else.
type Store struct {
	db *sql.DB
}

// NewStore binds the store to an already-open, already-migrated database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// ResolveOrMint returns the canonical masterId for email, running INSIDE the caller's
// transaction so it commits atomically with the inbox dedupe + the identity.resolved
// outbox enqueue. Known email -> the existing id (minted=false); unknown -> the
// candidate id is persisted (minted=true).
//
// It is race-safe by construction: a single `INSERT ... ON CONFLICT(email) DO NOTHING`
// is atomic, so two concurrent lookups for the same NEW email cannot both insert — the
// first wins (rows affected = 1), the loser's INSERT is a no-op (rows affected = 0) and
// the subsequent SELECT returns the winner's id. There is no SELECT-then-INSERT
// time-of-check/time-of-use gap, so exactly one masterId is ever minted per email.
func (s *Store) ResolveOrMint(ctx context.Context, tx *sql.Tx, email, candidateID, now string) (masterID string, minted bool, err error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO identities (master_id, email, status, created_at, updated_at)
		 VALUES (?, ?, 'active', ?, ?)
		 ON CONFLICT(email) DO NOTHING`,
		candidateID, email, now, now)
	if err != nil {
		return "", false, fmt.Errorf("insert identity for %q: %w", email, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("rows affected for %q: %w", email, err)
	}

	if err := tx.QueryRowContext(ctx,
		`SELECT master_id FROM identities WHERE email = ?`, email).Scan(&masterID); err != nil {
		return "", false, fmt.Errorf("select identity for %q: %w", email, err)
	}
	return masterID, affected == 1, nil
}
