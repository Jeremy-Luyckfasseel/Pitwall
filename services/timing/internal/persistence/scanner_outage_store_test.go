package persistence

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func openScannerOutageTestDB(t *testing.T) *ScannerOutageStore {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "timing.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if !tableExists(t, db, "scanner_outages") {
		t.Fatalf("scanner_outages table missing after migrate")
	}
	return NewScannerOutageStore(db)
}

// OpenOutage persists an open outage (online_at NULL) and returns its row id so recovery
// can close exactly that row. The durable "flag the gap" record (Story 3.5, Q38.2).
func TestOpenOutage_PersistsOpenRow(t *testing.T) {
	ctx := context.Background()
	s := openScannerOutageTestDB(t)

	id, err := s.OpenOutage(ctx, "start-finish", "sim-A",
		"2026-06-01T14:11:52.900Z", "2026-06-01T14:12:07.250Z", "2026-06-01T14:12:07.300Z")
	if err != nil {
		t.Fatalf("OpenOutage: %v", err)
	}
	if id <= 0 {
		t.Fatalf("OpenOutage id = %d, want > 0", id)
	}

	var scannerID, sessionID, gapFrom, since, recordedAt string
	var onlineAt sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT scanner_id, session_id, gap_from, since, online_at, recorded_at FROM scanner_outages WHERE id = ?`, id).
		Scan(&scannerID, &sessionID, &gapFrom, &since, &onlineAt, &recordedAt)
	if err != nil {
		t.Fatalf("query row: %v", err)
	}
	if scannerID != "start-finish" {
		t.Errorf("scannerID = %q, want %q", scannerID, "start-finish")
	}
	if sessionID != "sim-A" {
		t.Errorf("sessionID = %q, want %q", sessionID, "sim-A")
	}
	if gapFrom != "2026-06-01T14:11:52.900Z" {
		t.Errorf("gapFrom = %q, want %q", gapFrom, "2026-06-01T14:11:52.900Z")
	}
	if since != "2026-06-01T14:12:07.250Z" {
		t.Errorf("since = %q, want %q", since, "2026-06-01T14:12:07.250Z")
	}
	if onlineAt.Valid {
		t.Errorf("online_at = %q, want NULL (outage still open)", onlineAt.String)
	}
	if recordedAt != "2026-06-01T14:12:07.300Z" {
		t.Errorf("recordedAt = %q, want %q", recordedAt, "2026-06-01T14:12:07.300Z")
	}
}

// CloseOutage sets online_at on the row opened by OpenOutage (recovery).
func TestCloseOutage_SetsOnlineAt(t *testing.T) {
	ctx := context.Background()
	s := openScannerOutageTestDB(t)

	id, err := s.OpenOutage(ctx, "start-finish", "sim-A",
		"2026-06-01T14:11:52.900Z", "2026-06-01T14:12:07.250Z", "2026-06-01T14:12:07.300Z")
	if err != nil {
		t.Fatalf("OpenOutage: %v", err)
	}
	if err := s.CloseOutage(ctx, id, "2026-06-01T14:12:41.600Z"); err != nil {
		t.Fatalf("CloseOutage: %v", err)
	}

	var onlineAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT online_at FROM scanner_outages WHERE id = ?`, id).Scan(&onlineAt); err != nil {
		t.Fatalf("query online_at: %v", err)
	}
	if !onlineAt.Valid || onlineAt.String != "2026-06-01T14:12:41.600Z" {
		t.Fatalf("online_at = %v, want %q", onlineAt, "2026-06-01T14:12:41.600Z")
	}
}

// CloseOutage for an unknown id is surfaced as an error — a flagged gap's recovery is
// never silently lost (CLAUDE.md §2.6).
func TestCloseOutage_UnknownIdErrors(t *testing.T) {
	ctx := context.Background()
	s := openScannerOutageTestDB(t)

	err := s.CloseOutage(ctx, 9999, "2026-06-01T14:12:41.600Z")
	if err == nil {
		t.Fatalf("CloseOutage(unknown id) = nil, want error")
	}
}

// Two outages in one session are two independent rows (append), each closable on its own id.
func TestOpenOutage_TwoOutagesAreTwoRows(t *testing.T) {
	ctx := context.Background()
	s := openScannerOutageTestDB(t)

	id1, err := s.OpenOutage(ctx, "start-finish", "sim-A",
		"2026-06-01T14:11:52.900Z", "2026-06-01T14:12:07.250Z", "2026-06-01T14:12:07.300Z")
	if err != nil {
		t.Fatalf("OpenOutage #1: %v", err)
	}
	id2, err := s.OpenOutage(ctx, "start-finish", "sim-A",
		"2026-06-01T14:20:00.000Z", "2026-06-01T14:20:10.000Z", "2026-06-01T14:20:10.050Z")
	if err != nil {
		t.Fatalf("OpenOutage #2: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("two OpenOutage calls returned the same id %d (want distinct rows)", id1)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scanner_outages WHERE session_id = ?`, "sim-A").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2 (append, no dedupe)", count)
	}
}
