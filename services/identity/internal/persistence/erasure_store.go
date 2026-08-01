package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErasureStore is Identity's erasure-side facade over the identities table plus the two
// small tables Story 2.6 adds: identity_tombstones (masterId -> "this was erased here")
// and email_suppressions (an irreversible SHA-256 hash of the normalized email that was
// erased -> blocks a later lookup for the same address from silently re-minting,
// Round 33/Q33.1). It is stateless — every method runs inside the caller's transaction,
// matching the erasure.SliceDeleter/TombstoneWriter callback shape
// (libs/go-pitwall/erasure) these methods are wired into by cmd/identity/main.go.
type ErasureStore struct{}

// NewErasureStore constructs the (stateless) erasure store.
func NewErasureStore() *ErasureStore { return &ErasureStore{} }

// LookupEmail reads the CURRENT email for masterID, inside the caller's transaction.
// found=false (not an error) means the row is already gone — either a genuinely unknown
// masterId, or an idempotent double-erasure (a second privacy.erasure_requested for an
// already-erased identity, arriving under a different envelope id than the first, so the
// inbox dedupe does not catch it). The caller must treat found=false as a graceful no-op,
// never a failure.
func (s *ErasureStore) LookupEmail(ctx context.Context, tx *sql.Tx, masterID string) (email string, found bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT email FROM identities WHERE master_id = ?`, masterID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup email for %s: %w", masterID, err)
	}
	return email, true, nil
}

// DeleteSlice erases Identity's local slice for masterID: it suppresses emailHash (the
// caller's already-computed SHA-256 hash of the normalized email — see domain.HashEmail)
// so a later lookup for the same address is recognized (AC2), then deletes the
// identities row (AC1's "fully deletes", Q16.4). Both writes are idempotent
// (INSERT OR IGNORE / a DELETE of an already-absent row is a no-op), so a duplicate
// erasure attempt is always safe.
func (s *ErasureStore) DeleteSlice(ctx context.Context, tx *sql.Tx, masterID, emailHash, at string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO email_suppressions (email_hash, tombstoned_at) VALUES (?, ?)`,
		emailHash, at); err != nil {
		return fmt.Errorf("suppress email for %s: %w", masterID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM identities WHERE master_id = ?`, masterID); err != nil {
		return fmt.Errorf("delete identities row for %s: %w", masterID, err)
	}
	return nil
}

// WriteTombstone records the durable "this masterId was erased here" audit row
// (AC1/DG-7). INSERT OR IGNORE: a repeat write (idempotent double-erasure) stays one row.
func (s *ErasureStore) WriteTombstone(ctx context.Context, tx *sql.Tx, masterID, at string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO identity_tombstones (master_id, tombstoned_at) VALUES (?, ?)`,
		masterID, at)
	if err != nil {
		return fmt.Errorf("write tombstone for %s: %w", masterID, err)
	}
	return nil
}

// IsEmailSuppressed reports whether emailHash was suppressed by a prior erasure
// (Round 33/Q33.1) — the gate TxResolver.Resolve consults before ever minting.
func (s *ErasureStore) IsEmailSuppressed(ctx context.Context, tx *sql.Tx, emailHash string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM email_suppressions WHERE email_hash = ?`, emailHash).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check email suppression for hash %s: %w", emailHash, err)
	}
	return true, nil
}
