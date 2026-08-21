package domain

import "testing"

// ptr is a tiny helper to build an *int64 for the "current PR" argument.
func ptr(v int64) *int64 { return &v }

// AC1 / Q37.2: the FIRST-ever lap (no current PR) is a break with no previousMs.
func TestCheckPR_FirstEverLapIsABreakWithNoPrevious(t *testing.T) {
	broken, previous := CheckPR(nil, 42318)
	if !broken {
		t.Fatalf("first-ever lap must be a break (no PR yet), got broken=false")
	}
	if previous != nil {
		t.Errorf("first-ever break must carry no previousMs, got %d", *previous)
	}
}

// AC1: a lap strictly faster than the current PR is a break carrying the beaten value.
func TestCheckPR_FasterLapBreaksAndReportsPrevious(t *testing.T) {
	broken, previous := CheckPR(ptr(42318), 41980)
	if !broken {
		t.Fatalf("a faster lap must be a break")
	}
	if previous == nil || *previous != 42318 {
		t.Errorf("previousMs = %v, want 42318 (the value beaten)", previous)
	}
}

// A slower lap is not a break.
func TestCheckPR_SlowerLapIsNotABreak(t *testing.T) {
	broken, previous := CheckPR(ptr(41980), 42318)
	if broken {
		t.Errorf("a slower lap must not break the PR")
	}
	if previous != nil {
		t.Errorf("a non-break must report no previousMs, got %d", *previous)
	}
}

// A tie (equal to the current PR) does NOT beat it (strictly-less-than only).
func TestCheckPR_TieIsNotABreak(t *testing.T) {
	broken, _ := CheckPR(ptr(41980), 41980)
	if broken {
		t.Errorf("a lap equal to the PR must not break it (ties don't beat)")
	}
}
