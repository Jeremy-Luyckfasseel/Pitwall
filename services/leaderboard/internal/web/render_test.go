package web

import (
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
)

// AC3/AC1: the snapshot is ordered, positioned, fastest-flagged, with the
// short-masterId display fallback and a formatted (mono/tabular) lap time.
func TestToSnapshot_OrdersPositionsAndFlags(t *testing.T) {
	snap := ToSnapshot([]domain.DriverBest{
		{MasterID: "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", BestLapMs: 41000, BestLapAt: "2026-06-08T10:00:01.000Z", BestLapSeq: 1},
		{MasterID: "2b8e6d31-4f95-4e22-8bb3-6c7d5e4f3a10", BestLapMs: 42000, BestLapAt: "2026-06-08T10:00:02.000Z", BestLapSeq: 2},
	})
	if len(snap.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(snap.Rows))
	}
	r0 := snap.Rows[0]
	if r0.Position != 1 || !r0.IsFastest {
		t.Errorf("rank-1 row = %+v, want Position 1 + IsFastest", r0)
	}
	if r0.DisplayName != "1a9f7c20" {
		t.Errorf("DisplayName = %q, want short masterId 1a9f7c20", r0.DisplayName)
	}
	if r0.LapTimeMs != 41000 {
		t.Errorf("LapTimeMs = %d, want 41000", r0.LapTimeMs)
	}
	if r0.LapTime == "" {
		t.Error("LapTime (formatted) must be non-empty for mono/tabular rendering")
	}
	if snap.Rows[1].IsFastest {
		t.Error("only rank 1 may be flagged fastest")
	}
}

func TestFormatLapTime(t *testing.T) {
	cases := map[int64]string{
		42318:  "0:42.318",
		102318: "1:42.318",
		1001:   "0:01.001",
		60000:  "1:00.000",
	}
	for ms, want := range cases {
		if got := formatLapTime(ms); got != want {
			t.Errorf("formatLapTime(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestToSnapshot_Empty(t *testing.T) {
	snap := ToSnapshot(nil)
	if snap.Rows == nil {
		t.Error("Rows should be a non-nil (empty) slice so it serializes as [] not null")
	}
	if len(snap.Rows) != 0 {
		t.Errorf("want 0 rows, got %d", len(snap.Rows))
	}
}
