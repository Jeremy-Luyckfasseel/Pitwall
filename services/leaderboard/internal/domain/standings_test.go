package domain

import "testing"

func TestImprovesBest(t *testing.T) {
	// First lap for a driver always initializes the best.
	if !ImprovesBest(nil, Lap{LapTimeMs: 42000}) {
		t.Error("first lap must initialize the best")
	}
	prev := &DriverBest{MasterID: "a", BestLapMs: 42000, BestLapAt: "2026-06-08T10:00:00.000Z", BestLapSeq: 1}
	// A strictly faster lap improves.
	if !ImprovesBest(prev, Lap{LapTimeMs: 41999}) {
		t.Error("a strictly faster lap must improve the best")
	}
	// An equal lap does NOT improve (keeps the FIRST time it was set — tie-break).
	if ImprovesBest(prev, Lap{LapTimeMs: 42000}) {
		t.Error("an equal lap must NOT replace the best (first-to-set wins)")
	}
	// A slower lap does not improve.
	if ImprovesBest(prev, Lap{LapTimeMs: 43000}) {
		t.Error("a slower lap must not change the best")
	}
}

// AC1: standings ordered by best lap ascending; rank 1 marked fastest.
func TestRank_OrdersByBestLapAscending_FastestFlagged(t *testing.T) {
	rows := Rank([]DriverBest{
		{MasterID: "slow", BestLapMs: 43000, BestLapAt: "2026-06-08T10:00:03.000Z", BestLapSeq: 3},
		{MasterID: "fast", BestLapMs: 41000, BestLapAt: "2026-06-08T10:00:01.000Z", BestLapSeq: 1},
		{MasterID: "mid", BestLapMs: 42000, BestLapAt: "2026-06-08T10:00:02.000Z", BestLapSeq: 2},
	})
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	wantOrder := []string{"fast", "mid", "slow"}
	for i, w := range wantOrder {
		if rows[i].MasterID != w {
			t.Errorf("position %d = %q, want %q", i+1, rows[i].MasterID, w)
		}
		if rows[i].Position != i+1 {
			t.Errorf("row %d Position = %d, want %d", i, rows[i].Position, i+1)
		}
	}
	if !rows[0].IsFastest {
		t.Error("rank-1 row must be flagged IsFastest")
	}
	if rows[1].IsFastest || rows[2].IsFastest {
		t.Error("only the rank-1 row may be flagged IsFastest")
	}
}

// AC2: equal best lap → whoever set it FIRST (earlier wire `at`) ranks higher.
func TestRank_TieBreak_EarlierAtRanksHigher(t *testing.T) {
	rows := Rank([]DriverBest{
		{MasterID: "later", BestLapMs: 42000, BestLapAt: "2026-06-08T10:00:05.000Z", BestLapSeq: 9},
		{MasterID: "earlier", BestLapMs: 42000, BestLapAt: "2026-06-08T10:00:02.000Z", BestLapSeq: 4},
	})
	if rows[0].MasterID != "earlier" {
		t.Errorf("the driver who set the equal time FIRST must rank higher; got %q at rank 1", rows[0].MasterID)
	}
}

// AC2 (stability): equal best AND identical `at` → earlier ingest seq wins, so
// ordering is deterministic and never flaps.
func TestRank_TieBreak_EqualAt_FallsBackToIngestSeq(t *testing.T) {
	rows := Rank([]DriverBest{
		{MasterID: "secondSeen", BestLapMs: 42000, BestLapAt: "2026-06-08T10:00:02.000Z", BestLapSeq: 8},
		{MasterID: "firstSeen", BestLapMs: 42000, BestLapAt: "2026-06-08T10:00:02.000Z", BestLapSeq: 7},
	})
	if rows[0].MasterID != "firstSeen" {
		t.Errorf("on identical time AND `at`, the earlier-ingested driver must rank higher; got %q", rows[0].MasterID)
	}
}

func TestRank_Empty(t *testing.T) {
	if rows := Rank(nil); len(rows) != 0 {
		t.Errorf("Rank(nil) = %d rows, want 0", len(rows))
	}
}

func TestShortMasterID(t *testing.T) {
	got := ShortMasterID("1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")
	if got != "1a9f7c20" {
		t.Errorf("ShortMasterID = %q, want first uuid segment 1a9f7c20", got)
	}
	// Defensive: a short/empty id is returned as-is (never panics).
	if ShortMasterID("") != "" {
		t.Errorf("ShortMasterID(\"\") must be empty")
	}
	if ShortMasterID("abc") != "abc" {
		t.Errorf("ShortMasterID(short) must return it unchanged")
	}
}
