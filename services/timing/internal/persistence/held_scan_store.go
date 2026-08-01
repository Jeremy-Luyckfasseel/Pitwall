package persistence

import (
	"context"
	"database/sql"
	"fmt"
)

// HeldLineScanStore is Timing's durable "hold + flag, never dropped" sink for the
// register-first/unknown-token operator exception (FR39, Story 2.5): a start-finish
// crossing whose token had no completed check-in this session is recorded here
// instead of becoming a lap.recorded. Append-only — there is no read/update/delete
// API; a future operator late-binding capability (Epic 11) would add its own query
// need then.
type HeldLineScanStore struct {
	db *sql.DB
}

// NewHeldLineScanStore binds the store to an already-open, already-migrated database.
func NewHeldLineScanStore(db *sql.DB) *HeldLineScanStore {
	return &HeldLineScanStore{db: db}
}

// Record durably persists one held scan attempt. Every call inserts a new row — no
// dedupe/upsert — since each is a distinct real-world scan, even a repeat of the same
// stray token.
func (s *HeldLineScanStore) Record(ctx context.Context, token, method, sessionID, occurredAt, reason, recordedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO held_line_scans (token, method, session_id, occurred_at, reason, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		token, method, sessionID, occurredAt, reason, recordedAt)
	if err != nil {
		return fmt.Errorf("record held line scan %q: %w", token, err)
	}
	return nil
}
