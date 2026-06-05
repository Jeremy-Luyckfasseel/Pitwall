// Package domain holds Timing's pure race-capture rules — no I/O, no bus, no DB.
// The crossing -> lap rule turns a driver's start-finish crossings into counted
// laps: the first crossing is the out-lap (start marker, not counted), each
// subsequent crossing is one lap, and a crossing that arrives closer than
// MinLapTimeMs to the previous valid one is a bounce/duplicate read that is
// rejected and ignored (Story 1.6, FR35). Keeping these rules pure keeps them
// exhaustively unit-testable. Session lifecycle state (1.8) lands later.
package domain

import "time"

// Lap is one counted lap produced from a valid start-finish crossing.
type Lap struct {
	LapNumber int   // 1-based; the out-lap (first crossing) is not counted
	LapTimeMs int64 // milliseconds since this driver's previous valid crossing
}

// Outcome classifies what a crossing produced. A bounce (Rejected) must be
// distinguishable from the out-lap (StartMarker): both yield no lap, but a bounce
// is a filtered duplicate worth logging, whereas the start marker is the normal
// first crossing.
type Outcome int

const (
	// StartMarker is the driver's first crossing (out-lap): recorded to start the
	// clock, but not counted and emitting no lap.
	StartMarker Outcome = iota
	// Rejected is a crossing closer than MinLapTimeMs to the previous valid
	// crossing — a bounce/duplicate read. It is ignored: no lap, and the
	// previous-valid-crossing baseline is left untouched.
	Rejected
	// Counted is a valid lap.
	Counted
)

func (o Outcome) String() string {
	switch o {
	case StartMarker:
		return "StartMarker"
	case Rejected:
		return "Rejected"
	case Counted:
		return "Counted"
	default:
		return "Unknown"
	}
}

// LapTracker turns one driver's start-finish crossings into counted laps. The
// FIRST crossing is the out-lap / start marker: it is recorded but produces no
// lap (you cannot time a lap with no previous crossing). Each subsequent crossing
// is checked against the minimum-lap-time filter: if its delta from the previous
// valid crossing is below MinLapTimeMs it is a bounce/duplicate read and is
// Rejected (ignored, baseline unmoved); otherwise it yields one lap with
// lapTimeMs = delta from the previous valid crossing (FR34/FR35).
//
// State is per driver — hold one LapTracker per driver so one driver's crossing
// (valid or rejected) never affects another's. MinLapTimeMs == 0 disables the
// filter (every non-out-lap crossing counts), which is the pre-1.6 behaviour.
type LapTracker struct {
	// MinLapTimeMs is the minimum-lap-time filter threshold (FR35). A crossing
	// whose delta from the previous valid crossing is strictly less than this is
	// rejected as a bounce/duplicate. 0 disables the filter.
	MinLapTimeMs int64

	prev     time.Time
	hasPrev  bool
	lapCount int
}

// Cross records a crossing at time t and reports what it produced:
//   - StartMarker for the driver's first crossing (out-lap; no lap);
//   - Rejected for a bounce/duplicate (delta < MinLapTimeMs; no lap, baseline
//     left untouched so the next real lap is timed from the last valid crossing);
//   - Counted with the resulting Lap otherwise.
func (l *LapTracker) Cross(t time.Time) (Lap, Outcome) {
	if !l.hasPrev {
		l.prev = t
		l.hasPrev = true
		return Lap{}, StartMarker
	}
	delta := t.Sub(l.prev).Milliseconds()
	if l.MinLapTimeMs > 0 && delta < l.MinLapTimeMs {
		// Bounce/duplicate: ignore it entirely. Do NOT advance prev or lapCount —
		// it is a spurious re-detection of the same physical crossing.
		return Lap{}, Rejected
	}
	l.prev = t
	l.lapCount++
	return Lap{LapNumber: l.lapCount, LapTimeMs: delta}, Counted
}
