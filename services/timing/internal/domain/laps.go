// Package domain holds Timing's pure race-capture rules — no I/O, no bus, no DB.
// Story 1.5 introduces the crossing -> lap rule the simulator drives; later
// stories add the minimum-lap-time validity filter (1.6) and session lifecycle
// state (1.8). Keeping these rules pure keeps them exhaustively unit-testable.
package domain

import "time"

// Lap is one counted lap produced from a start-finish crossing.
type Lap struct {
	LapNumber int   // 1-based; the out-lap (first crossing) is not counted
	LapTimeMs int64 // milliseconds since this driver's previous valid crossing
}

// LapTracker turns one driver's start-finish crossings into counted laps. The
// FIRST crossing is the out-lap / start marker: it is recorded but produces no
// lap (you cannot time a lap with no previous crossing). Each subsequent
// crossing yields exactly one lap with lapTimeMs = delta from the previous
// valid crossing (FR34/FR35). State is per driver — hold one LapTracker per
// driver so one driver's crossing never affects another's.
//
// The minimum-lap-time bounce/duplicate filter is deliberately NOT here — that
// is Story 1.6. The simulator (Story 1.5) only generates clean, above-threshold
// crossings, so every non-out-lap crossing is a real lap.
type LapTracker struct {
	prev     time.Time
	hasPrev  bool
	lapCount int
}

// Cross records a crossing at time t. It returns (Lap, true) for a counted lap,
// or (zero, false) for the out-lap (the driver's first crossing).
func (l *LapTracker) Cross(t time.Time) (Lap, bool) {
	if !l.hasPrev {
		l.prev = t
		l.hasPrev = true
		return Lap{}, false
	}
	delta := t.Sub(l.prev).Milliseconds()
	l.prev = t
	l.lapCount++
	return Lap{LapNumber: l.lapCount, LapTimeMs: delta}, true
}
