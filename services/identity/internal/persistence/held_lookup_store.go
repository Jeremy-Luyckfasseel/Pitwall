package persistence

import (
	"context"
	"database/sql"
	"fmt"
)

// HeldLookupStore durably captures an identity.lookup_requested that was HELD because
// its email hash is suppressed by a prior erasure (AC2, Round 33/Q33.1) — the
// "never mints, never replies, never drops" sink, structurally identical to Story 2.5's
// HeldLineScanStore. Unlike that store, Record runs INSIDE the caller's transaction: it
// must commit atomically with TxResolver's inbox-mark (a crash between the two must
// never leave a held attempt un-recorded but the inbox already marked seen, which would
// silently swallow the hold on any redelivery).
type HeldLookupStore struct{}

// NewHeldLookupStore constructs the (stateless) held-lookup store.
func NewHeldLookupStore() *HeldLookupStore { return &HeldLookupStore{} }

// Record persists one held attempt. A plain INSERT, no upsert/dedupe — every held
// attempt (even a repeat for the same suppressed email) is its own row.
func (s *HeldLookupStore) Record(ctx context.Context, tx *sql.Tx, requestID, emailHash, reason, occurredAt, recordedAt string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO held_lookups (request_id, email_hash, reason, occurred_at, recorded_at)
		 VALUES (?, ?, ?, ?, ?)`,
		requestID, emailHash, reason, occurredAt, recordedAt)
	if err != nil {
		return fmt.Errorf("record held lookup for request %s: %w", requestID, err)
	}
	return nil
}
