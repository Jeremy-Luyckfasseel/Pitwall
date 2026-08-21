package persistence

import (
	"context"
	"path/filepath"
	"testing"
)

const (
	prMasterA = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
	prMasterB = "2b8e6d31-4f95-4c22-8bb3-6c7d5e4f3a10"
)

func openPRStore(t *testing.T) *DriverPRStore {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "timing.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if !tableExists(t, db, "driver_prs") {
		t.Fatalf("driver_prs table missing after migrate")
	}
	return NewDriverPRStore(db)
}

// AC1 / Q37.2: the first-ever lap for a driver with no local PR is a break with no
// previousMs, and it seeds the local copy.
func TestObserveLap_FirstLapIsABreakAndSeeds(t *testing.T) {
	ctx := context.Background()
	s := openPRStore(t)
	broken, previous, err := s.ObserveLap(ctx, prMasterA, "sess-1", 42318, "2026-06-05T14:00:10.000Z")
	if err != nil {
		t.Fatalf("ObserveLap: %v", err)
	}
	if !broken {
		t.Fatalf("first lap must be a break")
	}
	if previous != nil {
		t.Errorf("first break must carry no previousMs, got %d", *previous)
	}
	got, ok, err := s.Get(ctx, prMasterA)
	if err != nil || !ok {
		t.Fatalf("Get after first lap: ok=%v err=%v", ok, err)
	}
	if got != 42318 {
		t.Errorf("stored PR = %d, want 42318", got)
	}
}

// AC1 / Q37.3: a faster lap breaks and advances the local copy optimistically, so the
// NEXT lap compares against the new best (a subsequent slower-than-new lap is no break).
func TestObserveLap_OptimisticAdvancePerBreak(t *testing.T) {
	ctx := context.Background()
	s := openPRStore(t)
	s.ObserveLap(ctx, prMasterA, "sess-1", 42318, "2026-06-05T14:00:10.000Z") // first PR
	broken, previous, err := s.ObserveLap(ctx, prMasterA, "sess-1", 41980, "2026-06-05T14:01:10.000Z")
	if err != nil {
		t.Fatalf("ObserveLap: %v", err)
	}
	if !broken || previous == nil || *previous != 42318 {
		t.Fatalf("second (faster) lap: broken=%v previous=%v, want break beating 42318", broken, previous)
	}
	// A third lap slower than the NEW best (41980) is not a break — the copy advanced.
	broken, _, _ = s.ObserveLap(ctx, prMasterA, "sess-1", 42000, "2026-06-05T14:02:10.000Z")
	if broken {
		t.Errorf("a lap slower than the advanced local copy must not break")
	}
	got, _, _ := s.Get(ctx, prMasterA)
	if got != 41980 {
		t.Errorf("stored PR = %d, want 41980 (advanced best)", got)
	}
}

// A slower-than-current lap is not a break and does not move the copy.
func TestObserveLap_SlowerLapNoBreakNoWrite(t *testing.T) {
	ctx := context.Background()
	s := openPRStore(t)
	s.ObserveLap(ctx, prMasterA, "sess-1", 41980, "2026-06-05T14:00:10.000Z")
	broken, _, err := s.ObserveLap(ctx, prMasterA, "sess-1", 42500, "2026-06-05T14:01:10.000Z")
	if err != nil {
		t.Fatalf("ObserveLap: %v", err)
	}
	if broken {
		t.Errorf("slower lap must not break")
	}
	got, _, _ := s.Get(ctx, prMasterA)
	if got != 41980 {
		t.Errorf("stored PR = %d, want 41980 (unchanged)", got)
	}
}

// Per-driver isolation: one driver's PR never affects another's.
func TestObserveLap_PerDriverIsolation(t *testing.T) {
	ctx := context.Background()
	s := openPRStore(t)
	s.ObserveLap(ctx, prMasterA, "sess-1", 41980, "2026-06-05T14:00:10.000Z")
	brokenB, prevB, _ := s.ObserveLap(ctx, prMasterB, "sess-1", 50000, "2026-06-05T14:00:11.000Z")
	if !brokenB || prevB != nil {
		t.Errorf("driver B's first lap must be a break with no previous, got broken=%v prev=%v", brokenB, prevB)
	}
	gotA, _, _ := s.Get(ctx, prMasterA)
	if gotA != 41980 {
		t.Errorf("driver A PR = %d, want 41980 (untouched by B)", gotA)
	}
}

// AC2: Refresh overwrites the local copy with Driver's confirmed canonical value
// (latest-confirmed-wins), and seeds it if absent.
func TestRefresh_OverwritesAndSeeds(t *testing.T) {
	ctx := context.Background()
	s := openPRStore(t)
	// Seed via a break, then a confirmation lands a (here equal) canonical value.
	s.ObserveLap(ctx, prMasterA, "sess-1", 41980, "2026-06-05T14:00:10.000Z")
	if err := s.Refresh(ctx, prMasterA, 41000, "2026-06-05T14:00:10.000Z", "2026-06-05T14:00:12.000Z"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, _, _ := s.Get(ctx, prMasterA)
	if got != 41000 {
		t.Errorf("after Refresh, PR = %d, want 41000 (confirmed value wins)", got)
	}
	// Refresh for a driver with no local copy seeds it (never dropped).
	if err := s.Refresh(ctx, prMasterB, 39000, "2026-06-05T13:00:00.000Z", "2026-06-05T14:00:13.000Z"); err != nil {
		t.Fatalf("Refresh seed: %v", err)
	}
	gotB, okB, _ := s.Get(ctx, prMasterB)
	if !okB || gotB != 39000 {
		t.Errorf("Refresh for unknown driver must seed the copy, got ok=%v pr=%d", okB, gotB)
	}
}

// Get for an unknown driver returns ok=false (no PR yet), not an error.
func TestGet_UnknownDriver(t *testing.T) {
	ctx := context.Background()
	s := openPRStore(t)
	_, ok, err := s.Get(ctx, prMasterA)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Errorf("unknown driver must return ok=false")
	}
}
