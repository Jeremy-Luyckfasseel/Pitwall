package persistence

import (
	"context"
	"database/sql"
	"fmt"
)

// ScannerOutageStore is Timing's durable "flag the gap" record for a start-finish scanner
// outage (Story 3.5, FR38 / Q6.9 / Q38.2): when the scanner goes silent mid-session, prior
// laps are already persisted (persist-first) and the physically-missed crossings are
// acknowledged as a gap — never faked. Each outage is one row: OpenOutage inserts it (open,
// online_at NULL) and CloseOutage sets online_at on recovery. Append + single close-out; no
// delete API (retention is Story 3.7 / Epic 14). Mirrors HeldLineScanStore's posture.
type ScannerOutageStore struct {
	db *sql.DB
}

// NewScannerOutageStore binds the store to an already-open, already-migrated database.
func NewScannerOutageStore(db *sql.DB) *ScannerOutageStore {
	return &ScannerOutageStore{db: db}
}

// OpenOutage durably persists a newly-detected outage (online_at left NULL — still open) and
// returns its row id so the matching recovery can close exactly that row. gapFrom is the wire
// time of the last good crossing before the gap (= since when none yet this session); since is
// when the scanner was detected offline; recordedAt is when Timing persisted this row.
func (s *ScannerOutageStore) OpenOutage(ctx context.Context, scannerID, sessionID, gapFrom, since, recordedAt string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO scanner_outages (scanner_id, session_id, gap_from, since, recorded_at)
		 VALUES (?, ?, ?, ?, ?)`,
		scannerID, sessionID, gapFrom, since, recordedAt)
	if err != nil {
		return 0, fmt.Errorf("open scanner outage %q/%q: %w", scannerID, sessionID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("open scanner outage %q/%q: last insert id: %w", scannerID, sessionID, err)
	}
	return id, nil
}

// CloseOutage records recovery: it sets online_at on the row OpenOutage returned. An unknown
// id (no row updated) is surfaced as an error — a flagged gap's recovery is never silently
// lost (CLAUDE.md §2.6). Closing an already-closed row is a harmless overwrite of the same
// recovery time.
func (s *ScannerOutageStore) CloseOutage(ctx context.Context, id int64, onlineAt string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE scanner_outages SET online_at = ? WHERE id = ?`,
		onlineAt, id)
	if err != nil {
		return fmt.Errorf("close scanner outage id=%d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("close scanner outage id=%d: rows affected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("close scanner outage id=%d: no such outage row", id)
	}
	return nil
}
