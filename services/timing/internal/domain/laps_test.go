package domain

import (
	"testing"
	"time"
)

// minLapMs is the boundary used by the filter fixtures (the demo value-of-record
// is 10 s; here we just need a fixed threshold to assert MIN-1/MIN/MIN+1).
const minLapMs = 10000

func at(sec int) time.Time {
	return time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second)
}

func atMs(ms int) time.Time {
	return time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC).Add(time.Duration(ms) * time.Millisecond)
}

// AC1: the first crossing is the out-lap (start marker): recorded, but no lap.
func TestLapTracker_FirstCrossingIsStartMarker(t *testing.T) {
	var lt LapTracker
	if _, oc := lt.Cross(at(0)); oc != StartMarker {
		t.Fatalf("first crossing must be the out-lap (StartMarker), got %v", oc)
	}
}

// Each subsequent crossing yields one counted lap: 1-based lapNumber, lapTimeMs =
// delta from the previous valid crossing. (Zero-value tracker => filter off =>
// identical to Story 1.5 behaviour.)
func TestLapTracker_SubsequentCrossingsCountAndDelta(t *testing.T) {
	var lt LapTracker
	lt.Cross(at(0)) // out-lap

	lap, oc := lt.Cross(at(45)) // +45s
	if oc != Counted {
		t.Fatalf("second crossing should be Counted, got %v", oc)
	}
	if lap.LapNumber != 1 || lap.LapTimeMs != 45000 {
		t.Errorf("lap = {%d, %d}, want {1, 45000}", lap.LapNumber, lap.LapTimeMs)
	}

	lap, oc = lt.Cross(at(90)) // +45s
	if oc != Counted {
		t.Fatalf("third crossing should be Counted, got %v", oc)
	}
	if lap.LapNumber != 2 || lap.LapTimeMs != 45000 {
		t.Errorf("lap = {%d, %d}, want {2, 45000}", lap.LapNumber, lap.LapTimeMs)
	}
}

// Story 3.5: after a scanner outage, Reset makes the next crossing a fresh start marker
// (out-lap) so no counted lap ever spans the gap with an inflated, faked lap time (C1).
// Lap numbering continues (the driver's recorded-lap count is not lost).
func TestLapTracker_ResetMakesNextCrossingAStartMarker(t *testing.T) {
	var lt LapTracker
	lt.Cross(at(0))                     // out-lap
	if _, oc := lt.Cross(at(45)); oc != Counted {
		t.Fatalf("second crossing should be Counted, got %v", oc)
	}

	// Scanner goes offline; on recovery Reset is called so the resume crossing is a
	// fresh start marker (its delta from the pre-gap crossing — which would span the
	// outage — is never emitted as a lap).
	lt.Reset()
	if _, oc := lt.Cross(at(600)); oc != StartMarker {
		t.Fatalf("post-reset crossing should be a StartMarker, got %v", oc)
	}

	// The next crossing resumes counting, timed from the RESUME crossing (not across
	// the gap), and lap numbering continues at 2 (the pre-gap lap 1 is not lost).
	lap, oc := lt.Cross(at(645)) // +45s from the resume crossing
	if oc != Counted {
		t.Fatalf("crossing after reset+startmarker should be Counted, got %v", oc)
	}
	if lap.LapNumber != 2 || lap.LapTimeMs != 45000 {
		t.Errorf("lap = {%d, %d}, want {2, 45000} (timed from resume, numbering continues)", lap.LapNumber, lap.LapTimeMs)
	}
}

// AC2 boundary fixtures: relative to the previous valid crossing, MIN-1 is
// rejected as a bounce (and does NOT advance the baseline), exactly MIN and MIN+1
// are accepted.
func TestLapTracker_MinLapTimeBoundary_RejectsBelowAcceptsAtAndAbove(t *testing.T) {
	t.Run("MIN-1 rejected, baseline unmoved, then MIN+1 counted from the out-lap", func(t *testing.T) {
		lt := LapTracker{MinLapTimeMs: minLapMs}
		lt.Cross(atMs(0)) // out-lap; previous valid crossing = 0

		if lap, oc := lt.Cross(atMs(minLapMs - 1)); oc != Rejected {
			t.Fatalf("delta MIN-1 must be Rejected, got %v (lap %+v)", oc, lap)
		}
		// The bounce must not have advanced the baseline: a crossing at MIN+1 is
		// timed from the out-lap (0), so lapTimeMs == MIN+1 and lapNumber == 1.
		lap, oc := lt.Cross(atMs(minLapMs + 1))
		if oc != Counted {
			t.Fatalf("delta MIN+1 from the out-lap must be Counted, got %v", oc)
		}
		if lap.LapNumber != 1 || lap.LapTimeMs != minLapMs+1 {
			t.Errorf("lap = {%d, %d}, want {1, %d} (measured from the out-lap, not the bounce)",
				lap.LapNumber, lap.LapTimeMs, minLapMs+1)
		}
	})

	t.Run("exactly MIN is accepted (predicate is strictly-less)", func(t *testing.T) {
		lt := LapTracker{MinLapTimeMs: minLapMs}
		lt.Cross(atMs(0)) // out-lap
		lap, oc := lt.Cross(atMs(minLapMs))
		if oc != Counted || lap.LapNumber != 1 || lap.LapTimeMs != minLapMs {
			t.Errorf("delta exactly MIN: got {%d,%d} oc=%v, want {1,%d} Counted",
				lap.LapNumber, lap.LapTimeMs, oc, minLapMs)
		}
	})
}

// AC1 + AC2 coexistence: a bounce immediately after the out-lap is rejected and
// does NOT turn the out-lap into a counted lap; the next real crossing is lap 1.
func TestLapTracker_StartMarkerSurvivesAnImmediateBounce(t *testing.T) {
	lt := LapTracker{MinLapTimeMs: minLapMs}
	if _, oc := lt.Cross(atMs(0)); oc != StartMarker {
		t.Fatalf("first crossing must be StartMarker, got %v", oc)
	}
	if _, oc := lt.Cross(atMs(minLapMs - 1)); oc != Rejected {
		t.Fatalf("sub-MIN crossing right after the out-lap must be Rejected, got %v", oc)
	}
	lap, oc := lt.Cross(atMs(50000)) // a real lap, timed from the out-lap at 0
	if oc != Counted || lap.LapNumber != 1 || lap.LapTimeMs != 50000 {
		t.Errorf("first real lap = {%d,%d} oc=%v, want {1,50000} Counted", lap.LapNumber, lap.LapTimeMs, oc)
	}
}

// Two consecutive bounces are both rejected and neither moves the baseline.
func TestLapTracker_ConsecutiveBouncesDoNotAdvanceBaseline(t *testing.T) {
	lt := LapTracker{MinLapTimeMs: minLapMs}
	lt.Cross(atMs(0)) // out-lap
	if _, oc := lt.Cross(atMs(3000)); oc != Rejected {
		t.Fatalf("first bounce should be Rejected, got %v", oc)
	}
	if _, oc := lt.Cross(atMs(6000)); oc != Rejected {
		t.Fatalf("second bounce should be Rejected, got %v", oc)
	}
	lap, oc := lt.Cross(atMs(12000)) // measured from the out-lap at 0
	if oc != Counted || lap.LapNumber != 1 || lap.LapTimeMs != 12000 {
		t.Errorf("real lap after two bounces = {%d,%d} oc=%v, want {1,12000} Counted", lap.LapNumber, lap.LapTimeMs, oc)
	}
}

// AC3: validity is tracked PER driver — one tracker's crossings never reset
// another's (proven with two interleaved drivers, no filter).
func TestLapTracker_PerDriverIndependence(t *testing.T) {
	var a, b LapTracker
	a.Cross(at(0))  // A out-lap
	b.Cross(at(10)) // B out-lap (does NOT touch A)

	lapA, ocA := a.Cross(at(50)) // A lap 1 = 50s delta
	lapB, ocB := b.Cross(at(40)) // B lap 1 = 30s delta
	if ocA != Counted || ocB != Counted {
		t.Fatalf("both drivers should each produce a Counted lap (A=%v B=%v)", ocA, ocB)
	}
	if lapA.LapNumber != 1 || lapA.LapTimeMs != 50000 {
		t.Errorf("driver A lap = {%d, %d}, want {1, 50000}", lapA.LapNumber, lapA.LapTimeMs)
	}
	if lapB.LapNumber != 1 || lapB.LapTimeMs != 30000 {
		t.Errorf("driver B lap = {%d, %d}, want {1, 30000}", lapB.LapNumber, lapB.LapTimeMs)
	}
}

// AC3 with the filter active: a bounce on driver A must not touch driver B's
// state, and A's own baseline is unmoved by its bounce.
func TestLapTracker_PerDriverIndependenceWithBounce(t *testing.T) {
	a := LapTracker{MinLapTimeMs: minLapMs}
	b := LapTracker{MinLapTimeMs: minLapMs}
	a.Cross(atMs(0)) // A out-lap
	b.Cross(atMs(0)) // B out-lap

	if _, oc := a.Cross(atMs(minLapMs - 1)); oc != Rejected {
		t.Fatalf("A's bounce should be Rejected, got %v", oc)
	}
	// B's clean lap is unaffected by A's bounce.
	lapB, oc := b.Cross(atMs(50000))
	if oc != Counted || lapB.LapNumber != 1 || lapB.LapTimeMs != 50000 {
		t.Errorf("B lap = {%d,%d} oc=%v, want {1,50000} Counted", lapB.LapNumber, lapB.LapTimeMs, oc)
	}
	// A's next real crossing is still timed from A's out-lap (bounce didn't advance it).
	lapA, oc := a.Cross(atMs(50000))
	if oc != Counted || lapA.LapNumber != 1 || lapA.LapTimeMs != 50000 {
		t.Errorf("A lap = {%d,%d} oc=%v, want {1,50000} Counted", lapA.LapNumber, lapA.LapTimeMs, oc)
	}
}
