package dlq_test

import (
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/dlq"
)

var pol = dlq.Policy{MaxAttempts: 5, BaseMs: 1000, Multiplier: 2, MaxMs: 60000}

func TestNextRetry_BackoffThenPark(t *testing.T) {
	// priorRetries 0..3 retry with exponential backoff; at attempt == MaxAttempts park.
	cases := []struct {
		prior     int
		wantPark  bool
		wantDelay int
		wantNext  int
	}{
		{0, false, 1000, 1},
		{1, false, 2000, 2},
		{2, false, 4000, 3},
		{3, false, 8000, 4},
		{4, true, 0, 0}, // attemptsMade = 5 == MaxAttempts -> park
	}
	for _, c := range cases {
		got := dlq.NextRetry(c.prior, pol)
		if got.Park != c.wantPark {
			t.Fatalf("NextRetry(%d).Park = %v; want %v", c.prior, got.Park, c.wantPark)
		}
		if !c.wantPark && (got.DelayMs != c.wantDelay || got.NextRetries != c.wantNext) {
			t.Fatalf("NextRetry(%d) = %+v; want delay %d next %d", c.prior, got, c.wantDelay, c.wantNext)
		}
	}
}

func TestNextRetry_DelayClampedToMax(t *testing.T) {
	p := dlq.Policy{MaxAttempts: 100, BaseMs: 1000, Multiplier: 10, MaxMs: 5000}
	if got := dlq.NextRetry(3, p); got.DelayMs != 5000 {
		t.Fatalf("delay = %d; want clamped to 5000", got.DelayMs)
	}
}
