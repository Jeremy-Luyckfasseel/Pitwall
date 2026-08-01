package persistence

import (
	"context"
	"path/filepath"
	"testing"
)

func openHeldScanTestDB(t *testing.T) *HeldLineScanStore {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "timing.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if !tableExists(t, db, "held_line_scans") {
		t.Fatalf("held_line_scans table missing after migrate")
	}
	return NewHeldLineScanStore(db)
}

// Record durably persists a held line scan — the register-first/unknown-token
// operator exception (FR39, Story 2.5): a crossing whose token had no completed
// check-in this session. Every call is an append (no dedupe/upsert): a stray token
// scanned twice produces two rows, one per real-world scan attempt.
func TestRecordHeldLineScan_Persists(t *testing.T) {
	ctx := context.Background()
	s := openHeldScanTestDB(t)

	err := s.Record(ctx, "TP-STRAY-1", "transponder", "sim-20260801T120000.000Z",
		"2026-08-01T12:00:05.000Z", "no completed check-in this session", "2026-08-01T12:00:05.100Z")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT token, method, session_id, occurred_at, reason, recorded_at FROM held_line_scans`)
	if err != nil {
		t.Fatalf("query held_line_scans: %v", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		count++
		var token, method, sessionID, occurredAt, reason, recordedAt string
		if err := rows.Scan(&token, &method, &sessionID, &occurredAt, &reason, &recordedAt); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		if token != "TP-STRAY-1" {
			t.Errorf("token = %q, want %q", token, "TP-STRAY-1")
		}
		if method != "transponder" {
			t.Errorf("method = %q, want %q", method, "transponder")
		}
		if sessionID != "sim-20260801T120000.000Z" {
			t.Errorf("sessionID = %q, want %q", sessionID, "sim-20260801T120000.000Z")
		}
		if occurredAt != "2026-08-01T12:00:05.000Z" {
			t.Errorf("occurredAt = %q, want %q", occurredAt, "2026-08-01T12:00:05.000Z")
		}
		if reason != "no completed check-in this session" {
			t.Errorf("reason = %q, want %q", reason, "no completed check-in this session")
		}
		if recordedAt != "2026-08-01T12:00:05.100Z" {
			t.Errorf("recordedAt = %q, want %q", recordedAt, "2026-08-01T12:00:05.100Z")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
}

// Two scans of the same stray token are two independent rows — no dedupe/upsert.
func TestRecordHeldLineScan_RepeatedScanIsTwoRows(t *testing.T) {
	ctx := context.Background()
	s := openHeldScanTestDB(t)

	for i := 0; i < 2; i++ {
		if err := s.Record(ctx, "TP-STRAY-1", "transponder", "sim-A",
			"2026-08-01T12:00:00.000Z", "no completed check-in this session", "2026-08-01T12:00:00.100Z"); err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM held_line_scans WHERE token = ?`, "TP-STRAY-1").
		Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2 (append-only, no dedupe)", count)
	}
}
