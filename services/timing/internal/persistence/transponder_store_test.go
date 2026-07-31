package persistence

import (
	"context"
	"path/filepath"
	"testing"
)

const (
	tpMasterA = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
	tpMasterB = "2b8e6d31-4f95-4c22-8bb3-6c7d5e4f3a10"
)

func openTestDB(t *testing.T) *TransponderStore {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "timing.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if !tableExists(t, db, "transponder_map") {
		t.Fatalf("transponder_map table missing after migrate")
	}
	if !tableExists(t, db, "inbox") {
		t.Fatalf("inbox table missing after migrate")
	}
	return NewTransponderStore(db)
}

// A hardware id bound at hand-out resolves to its masterId at the gate (AC2).
func TestResolveTransponder_Known(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	if err := s.Upsert(ctx, "TP-001", tpMasterA, "2026-06-05T13:58:02.140Z"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, ok, err := s.Resolve(ctx, "TP-001")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatalf("Resolve(TP-001) ok=false, want true")
	}
	if got != tpMasterA {
		t.Errorf("Resolve(TP-001) = %q, want %q", got, tpMasterA)
	}
}

// An unknown hardware id resolves to ok=false — the caller MUST NOT mint an id
// (that unknown-token operator exception is Story 2.5).
func TestResolveTransponder_Unknown(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	got, ok, err := s.Resolve(ctx, "TP-does-not-exist")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Errorf("Resolve(unknown) ok=true, want false (got %q)", got)
	}
}

// Assign is the hand-out trigger (Story 2.4, FR33): a first-time hand-out reports
// reassigned=false (no prior mapping) and the mapping resolves afterward.
func TestAssignTransponder_FirstHandOut(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	reassigned, previous, err := s.Assign(ctx, "TP-100", tpMasterA, "2026-07-31T10:00:00.000Z")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if reassigned {
		t.Errorf("Assign(first hand-out) reassigned=true, want false")
	}
	if previous != "" {
		t.Errorf("Assign(first hand-out) previousMasterID=%q, want empty", previous)
	}
	got, ok, err := s.Resolve(ctx, "TP-100")
	if err != nil || !ok {
		t.Fatalf("Resolve after Assign: ok=%v err=%v", ok, err)
	}
	if got != tpMasterA {
		t.Errorf("Resolve(TP-100) = %q, want %q", got, tpMasterA)
	}
}

// Re-handing out to a DIFFERENT masterId is a reassignment: Assign reports it (AC2)
// and the latest mapping wins.
func TestAssignTransponder_Reassignment(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	if _, _, err := s.Assign(ctx, "TP-101", tpMasterA, "2026-07-31T10:00:00.000Z"); err != nil {
		t.Fatalf("first Assign: %v", err)
	}
	reassigned, previous, err := s.Assign(ctx, "TP-101", tpMasterB, "2026-07-31T11:00:00.000Z")
	if err != nil {
		t.Fatalf("second Assign: %v", err)
	}
	if !reassigned {
		t.Errorf("Assign(reassignment) reassigned=false, want true")
	}
	if previous != tpMasterA {
		t.Errorf("Assign(reassignment) previousMasterID=%q, want %q", previous, tpMasterA)
	}
	got, ok, err := s.Resolve(ctx, "TP-101")
	if err != nil || !ok {
		t.Fatalf("Resolve after reassign: ok=%v err=%v", ok, err)
	}
	if got != tpMasterB {
		t.Errorf("Resolve(TP-101) = %q, want the latest %q", got, tpMasterB)
	}
}

// Re-handing out to the SAME masterId (an idempotent replay of the hand-out trigger)
// must NOT report a reassignment — only an actual change of driver counts.
func TestAssignTransponder_IdempotentSameDriver(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	if _, _, err := s.Assign(ctx, "TP-102", tpMasterA, "2026-07-31T10:00:00.000Z"); err != nil {
		t.Fatalf("first Assign: %v", err)
	}
	reassigned, previous, err := s.Assign(ctx, "TP-102", tpMasterA, "2026-07-31T10:05:00.000Z")
	if err != nil {
		t.Fatalf("second Assign: %v", err)
	}
	if reassigned {
		t.Errorf("Assign(same driver replay) reassigned=true, want false")
	}
	if previous != "" {
		t.Errorf("Assign(same driver replay) previousMasterID=%q, want empty", previous)
	}
}

// Re-handing out the same transponder to a different driver via the raw Upsert seam
// (used by seeding/tests, not the hand-out trigger — see TestAssignTransponder_* above
// for Assign's reassignment reporting): the latest mapping wins.
func TestUpsertTransponder_LatestWins(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	if err := s.Upsert(ctx, "TP-007", tpMasterA, "2026-06-05T10:00:00.000Z"); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := s.Upsert(ctx, "TP-007", tpMasterB, "2026-06-05T11:00:00.000Z"); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	got, ok, err := s.Resolve(ctx, "TP-007")
	if err != nil || !ok {
		t.Fatalf("Resolve after reassign: ok=%v err=%v", ok, err)
	}
	if got != tpMasterB {
		t.Errorf("Resolve(TP-007) = %q, want the latest %q", got, tpMasterB)
	}
}
