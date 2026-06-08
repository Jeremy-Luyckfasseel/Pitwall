package persistence

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "lb.db")
	db, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func lap(master string, ms int64, at string, seq int64) domain.Lap {
	return domain.Lap{MasterID: master, LapTimeMs: ms, At: at, Seq: seq}
}

// AC1: a lap is applied once; the standings reflect the driver's best.
func TestApply_FirstLap_IsAppliedAndProjected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	applied, dup, err := s.Apply(ctx, "id-1", "lap.recorded", "2026-06-08T10:00:01.000Z",
		lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !applied || dup {
		t.Fatalf("first lap: applied=%v dup=%v, want applied=true dup=false", applied, dup)
	}
	bests, err := s.AllBests(ctx)
	if err != nil {
		t.Fatalf("AllBests: %v", err)
	}
	if len(bests) != 1 || bests[0].MasterID != "a" || bests[0].BestLapMs != 42000 {
		t.Fatalf("standings = %+v, want one row a@42000", bests)
	}
}

// AC1 (M6): a REDELIVERED envelope id is a no-op — not re-applied, read-model unchanged.
func TestApply_DuplicateEnvelopeID_IsNoOp(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	l := lap("a", 42000, "2026-06-08T10:00:01.000Z", 1)

	if _, _, err := s.Apply(ctx, "id-1", "lap.recorded", l.At, l); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	applied, dup, err := s.Apply(ctx, "id-1", "lap.recorded", l.At, l) // same id again
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if applied || !dup {
		t.Errorf("redelivered id: applied=%v dup=%v, want applied=false dup=true", applied, dup)
	}
	bests, _ := s.AllBests(ctx)
	if len(bests) != 1 || bests[0].BestLapMs != 42000 {
		t.Errorf("standings changed after a duplicate: %+v", bests)
	}
}

// AC1: a slower later lap does NOT worsen the driver's standing.
func TestApply_SlowerLap_DoesNotChangeBest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.Apply(ctx, "id-1", "lap.recorded", "t1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))

	applied, _, err := s.Apply(ctx, "id-2", "lap.recorded", "t2", lap("a", 43000, "2026-06-08T10:00:02.000Z", 2))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied {
		t.Error("a slower lap should report applied=false (no improvement)")
	}
	bests, _ := s.AllBests(ctx)
	if bests[0].BestLapMs != 42000 || bests[0].BestLapAt != "2026-06-08T10:00:01.000Z" {
		t.Errorf("best changed on a slower lap: %+v", bests[0])
	}
}

// A faster lap improves the best and records the new time + its `at`.
func TestApply_FasterLap_ImprovesBest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.Apply(ctx, "id-1", "lap.recorded", "t1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))

	applied, _, err := s.Apply(ctx, "id-2", "lap.recorded", "t2", lap("a", 41000, "2026-06-08T10:00:05.000Z", 2))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !applied {
		t.Error("a faster lap must be applied")
	}
	bests, _ := s.AllBests(ctx)
	if bests[0].BestLapMs != 41000 || bests[0].BestLapAt != "2026-06-08T10:00:05.000Z" {
		t.Errorf("best not improved: %+v", bests[0])
	}
}

// An equal lap keeps the FIRST time it was set (tie-break correctness in the store).
func TestApply_EqualLap_KeepsFirstAt(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.Apply(ctx, "id-1", "lap.recorded", "t1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))

	applied, _, _ := s.Apply(ctx, "id-2", "lap.recorded", "t2", lap("a", 42000, "2026-06-08T10:00:09.000Z", 2))
	if applied {
		t.Error("an equal lap must not be re-applied (first-to-set wins)")
	}
	bests, _ := s.AllBests(ctx)
	if bests[0].BestLapAt != "2026-06-08T10:00:01.000Z" {
		t.Errorf("BestLapAt moved on an equal lap: %+v", bests[0])
	}
}

// MaxSeq returns the highest stored ingest sequence (0 when empty) so the
// consumer can seed its counter across a restart and keep the tie-break
// sequence monotonic.
func TestMaxSeq(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if got, err := s.MaxSeq(ctx); err != nil || got != 0 {
		t.Fatalf("MaxSeq on empty = %d, %v; want 0, nil", got, err)
	}
	_, _, _ = s.Apply(ctx, "id-1", "lap.recorded", "t1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 5))
	_, _, _ = s.Apply(ctx, "id-2", "lap.recorded", "t2", lap("b", 41000, "2026-06-08T10:00:02.000Z", 9))
	if got, err := s.MaxSeq(ctx); err != nil || got != 9 {
		t.Errorf("MaxSeq = %d, %v; want 9, nil", got, err)
	}
}

// Two drivers each keep their own best (per-driver projection).
func TestApply_TwoDrivers_IndependentBests(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.Apply(ctx, "id-1", "lap.recorded", "t1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))
	_, _, _ = s.Apply(ctx, "id-2", "lap.recorded", "t2", lap("b", 41000, "2026-06-08T10:00:02.000Z", 2))

	bests, _ := s.AllBests(ctx)
	if len(bests) != 2 {
		t.Fatalf("want 2 drivers, got %d", len(bests))
	}
}
