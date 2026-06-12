package domain

import "testing"

// pinnedPolicy is the Story-1.9 production policy (Q&A Round 27): 5 attempts,
// 1 s base, ×2 per hop, 60 s ceiling.
var pinnedPolicy = DLQPolicy{MaxAttempts: 5, BaseMs: 1000, Multiplier: 2, MaxMs: 60000}

// TestNextRetryPinnedSchedule pins the exact escalation the user confirmed:
// retry delays 1 s → 2 s → 4 s → 8 s, then PARK on the 5th attempt.
func TestNextRetryPinnedSchedule(t *testing.T) {
	cases := []struct {
		priorRetries int
		wantPark     bool
		wantDelayMs  int
		wantNext     int
	}{
		{0, false, 1000, 1}, // 1st failure → retry in 1 s
		{1, false, 2000, 2}, // 2nd failure → retry in 2 s
		{2, false, 4000, 3}, // 3rd failure → retry in 4 s
		{3, false, 8000, 4}, // 4th failure → retry in 8 s
		{4, true, 0, 0},     // 5th attempt failed → park (cap reached)
	}
	for _, c := range cases {
		got := NextRetry(c.priorRetries, pinnedPolicy)
		if got.Park != c.wantPark {
			t.Errorf("NextRetry(%d).Park = %v, want %v", c.priorRetries, got.Park, c.wantPark)
		}
		if c.wantPark {
			continue // delay/next are meaningless once parked
		}
		if got.DelayMs != c.wantDelayMs {
			t.Errorf("NextRetry(%d).DelayMs = %d, want %d", c.priorRetries, got.DelayMs, c.wantDelayMs)
		}
		if got.NextRetries != c.wantNext {
			t.Errorf("NextRetry(%d).NextRetries = %d, want %d", c.priorRetries, got.NextRetries, c.wantNext)
		}
	}
}

// TestNextRetryParkImmediatelyWhenCapIsOne: MaxAttempts=1 means the very first
// failure is terminal — there is no retry hop at all.
func TestNextRetryParkImmediatelyWhenCapIsOne(t *testing.T) {
	p := DLQPolicy{MaxAttempts: 1, BaseMs: 1000, Multiplier: 2, MaxMs: 60000}
	if got := NextRetry(0, p); !got.Park {
		t.Errorf("NextRetry(0) with MaxAttempts=1 = %+v, want Park", got)
	}
}

// TestNextRetryClampsToMaxMs: exponential growth never exceeds the ceiling.
func TestNextRetryClampsToMaxMs(t *testing.T) {
	p := DLQPolicy{MaxAttempts: 10, BaseMs: 1000, Multiplier: 2, MaxMs: 3000}
	wantDelays := []int{1000, 2000, 3000, 3000, 3000} // 1000,2000,4000→3000,8000→3000,…
	for prior, want := range wantDelays {
		got := NextRetry(prior, p)
		if got.Park {
			t.Fatalf("NextRetry(%d) parked early with MaxAttempts=10", prior)
		}
		if got.DelayMs != want {
			t.Errorf("NextRetry(%d).DelayMs = %d, want %d (clamped to MaxMs)", prior, got.DelayMs, want)
		}
	}
}

// TestNextRetryConstantWhenMultiplierOne: ×1 degrades to a fixed delay.
func TestNextRetryConstantWhenMultiplierOne(t *testing.T) {
	p := DLQPolicy{MaxAttempts: 4, BaseMs: 500, Multiplier: 1, MaxMs: 60000}
	for prior := 0; prior < 3; prior++ {
		if got := NextRetry(prior, p); got.DelayMs != 500 {
			t.Errorf("NextRetry(%d).DelayMs = %d, want 500 (constant)", prior, got.DelayMs)
		}
	}
}
