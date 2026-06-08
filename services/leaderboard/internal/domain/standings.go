// Package domain holds the Leaderboard's pure, I/O-free read-model rules: the
// "does this lap improve a driver's best?" rule and the standings ordering with
// the first-to-set tie-break (FR41/FR42/FR44). It imports nothing outside the
// standard library and is exhaustively unit-tested; persistence/web are thin
// shells around it.
package domain

import (
	"sort"
	"strings"
)

// Lap is the minimal view of a consumed lap.recorded the standings rule needs.
// At is the canonical wire timestamp of the crossing; Seq is a monotonic ingest
// order assigned by the consumer (the deterministic final tie-break).
type Lap struct {
	MasterID  string
	LapTimeMs int64
	At        string
	Seq       int64
}

// DriverBest is a driver's current best lap in the read-model. BestLapAt is the
// wire timestamp of the lap that set the current best — the FIRST time it was
// achieved (the tie-break key). BestLapSeq breaks identical-At ties.
type DriverBest struct {
	MasterID   string
	BestLapMs  int64
	BestLapAt  string
	BestLapSeq int64
}

// RankedRow is one ordered standings row for rendering.
type RankedRow struct {
	Position  int
	MasterID  string
	BestLapMs int64
	IsFastest bool // true only for rank 1 (the fastest lap of the session)
}

// ImprovesBest reports whether lap improves (or initializes) the driver's best.
// The predicate is STRICTLY-less: an equal lap does NOT replace the best, so the
// recorded BestLapAt keeps the FIRST moment that time was achieved (FR44 tie-break).
func ImprovesBest(prev *DriverBest, lap Lap) bool {
	return prev == nil || lap.LapTimeMs < prev.BestLapMs
}

// Rank orders the driver bests into standings: primary best lap ascending;
// tie-break the lap set FIRST (earlier BestLapAt) ranks higher; identical At
// falls back to the earlier ingest sequence so ordering is deterministic and
// never flaps. The rank-1 row is flagged IsFastest. The input is not mutated.
func Rank(bests []DriverBest) []RankedRow {
	sorted := make([]DriverBest, len(bests))
	copy(sorted, bests)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.BestLapMs != b.BestLapMs {
			return a.BestLapMs < b.BestLapMs
		}
		if a.BestLapAt != b.BestLapAt {
			return a.BestLapAt < b.BestLapAt // lexicographic == chronological for the canonical wire format
		}
		return a.BestLapSeq < b.BestLapSeq
	})
	rows := make([]RankedRow, len(sorted))
	for i, b := range sorted {
		rows[i] = RankedRow{
			Position:  i + 1,
			MasterID:  b.MasterID,
			BestLapMs: b.BestLapMs,
			IsFastest: i == 0,
		}
	}
	return rows
}

// ShortMasterID returns the first segment of a canonical UUID masterId — the
// display-name fallback when no nickname exists (Epic 1 has no nicknames or
// racing numbers; Q26.3). A value with no '-' is returned unchanged.
func ShortMasterID(masterID string) string {
	if i := strings.IndexByte(masterID, '-'); i > 0 {
		return masterID[:i]
	}
	return masterID
}
