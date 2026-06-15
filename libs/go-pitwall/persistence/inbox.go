package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The idempotent inbox: dedupe on envelope id so an at-least-once redelivery is a
// safe no-op (M6). Both helpers run inside the CALLER's transaction so the dedupe
// check + the read-model write + the inbox insert commit together — a crash can
// neither double-apply nor apply-without-marking (the consumer-side mirror of the
// transactional outbox). They expect an `inbox` table with columns:
// (id TEXT PRIMARY KEY, type TEXT, processed_at TEXT) — the service supplies the DDL.

// InboxHasSeen reports whether the envelope id has already been processed.
func InboxHasSeen(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM inbox WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inbox lookup: %w", err)
	}
	return true, nil
}

// InboxMarkSeen records the envelope id as processed. It must be called within the
// same transaction that applied the message's effect.
func InboxMarkSeen(ctx context.Context, tx *sql.Tx, id, eventType, processedAt string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO inbox (id, type, processed_at) VALUES (?, ?, ?)`,
		id, eventType, processedAt)
	if err != nil {
		return fmt.Errorf("inbox insert: %w", err)
	}
	return nil
}
