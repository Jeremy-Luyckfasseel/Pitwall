package domain

import (
	"testing"
	"time"
)

func at(sec int) time.Time {
	return time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second)
}

// The first crossing is the out-lap (start marker): recorded, but no lap emitted.
func TestLapTracker_FirstCrossingIsStartMarker(t *testing.T) {
	var lt LapTracker
	if _, ok := lt.Cross(at(0)); ok {
		t.Fatal("first crossing must be the out-lap (no lap), got a lap")
	}
}

// Each subsequent crossing yields one lap: 1-based lapNumber, lapTimeMs = delta
// from the previous valid crossing.
func TestLapTracker_SubsequentCrossingsCountAndDelta(t *testing.T) {
	var lt LapTracker
	lt.Cross(at(0)) // out-lap

	lap, ok := lt.Cross(at(45)) // +45s
	if !ok {
		t.Fatal("second crossing should produce a lap")
	}
	if lap.LapNumber != 1 || lap.LapTimeMs != 45000 {
		t.Errorf("lap = {%d, %d}, want {1, 45000}", lap.LapNumber, lap.LapTimeMs)
	}

	lap, ok = lt.Cross(at(90)) // +45s
	if !ok {
		t.Fatal("third crossing should produce a lap")
	}
	if lap.LapNumber != 2 || lap.LapTimeMs != 45000 {
		t.Errorf("lap = {%d, %d}, want {2, 45000}", lap.LapNumber, lap.LapTimeMs)
	}
}

// Validity is tracked PER driver: one tracker's crossings never reset another's
// (FR35 — proven with two interleaved drivers).
func TestLapTracker_PerDriverIndependence(t *testing.T) {
	var a, b LapTracker
	a.Cross(at(0))  // A out-lap
	b.Cross(at(10)) // B out-lap (does NOT touch A)

	lapA, okA := a.Cross(at(50)) // A lap 1 = 50s delta
	lapB, okB := b.Cross(at(40)) // B lap 1 = 30s delta
	if !okA || !okB {
		t.Fatal("both drivers should each produce a lap")
	}
	if lapA.LapNumber != 1 || lapA.LapTimeMs != 50000 {
		t.Errorf("driver A lap = {%d, %d}, want {1, 50000}", lapA.LapNumber, lapA.LapTimeMs)
	}
	if lapB.LapNumber != 1 || lapB.LapTimeMs != 30000 {
		t.Errorf("driver B lap = {%d, %d}, want {1, 30000}", lapB.LapNumber, lapB.LapTimeMs)
	}
}
