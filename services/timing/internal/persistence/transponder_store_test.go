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

// Re-handing out the same transponder to a different driver: the latest mapping
// wins (the store supports it; the hand-out TRIGGER + logging is Story 2.4).
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
