package simulator

import (
	"context"
	"errors"
	"math/rand"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
)

type observeCall struct {
	masterID  string
	sessionID string
	lapTimeMs int64
}

// Story 3.4 (AC1): PR detection runs on the lap-production path. For each counted
// lap.recorded, Run calls ObservePR; on a break it enqueues a personal_record.broken
// immediately after that lap, carrying the lap's masterId/sessionId/lapTimeMs and the
// session's correlationId. A first-PR break (previousMs nil) omits previousMs.
func TestRun_ObservePR_EmitsBrokenOnBreak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := testConfig(rand.New(rand.NewSource(3)))
	cfg.Drivers = 1
	cfg.SessionLaps = 3

	var observed []observeCall
	brokenOnce := map[string]bool{} // break only the FIRST lap per driver (first PR)
	cfg.ObservePR = func(_ context.Context, masterID, sessionID string, lapTimeMs int64, at string) (bool, *int64, error) {
		observed = append(observed, observeCall{masterID, sessionID, lapTimeMs})
		if !brokenOnce[masterID] {
			brokenOnce[masterID] = true
			return true, nil, nil // first PR: no previousMs
		}
		return false, nil, nil
	}

	var got []messaging.Envelope
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		got = append(got, e)
		if e.Type == messaging.SessionEndedRoutingKey {
			cancel()
		}
		return nil
	}
	if err := New(cfg).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// ObservePR called once per counted lap (1 driver x 3 laps).
	if len(observed) != 3 {
		t.Fatalf("ObservePR calls = %d, want 3 (one per counted lap)", len(observed))
	}

	// Exactly one personal_record.broken (the first lap), positioned right after its lap.
	brokenIdx := -1
	brokenCount := 0
	for i, e := range got {
		if e.Type == messaging.PersonalRecordBrokenRoutingKey {
			brokenCount++
			brokenIdx = i
		}
	}
	if brokenCount != 1 {
		t.Fatalf("personal_record.broken count = %d, want 1", brokenCount)
	}
	if brokenIdx == 0 || got[brokenIdx-1].Type != messaging.LapRecordedRoutingKey {
		t.Errorf("personal_record.broken must be enqueued immediately after a lap.recorded")
	}

	prev := got[brokenIdx-1].Data.(messaging.LapRecordedData)
	d := got[brokenIdx].Data.(messaging.PersonalRecordBrokenData)
	if d.MasterID != prev.MasterID || d.SessionID != prev.SessionID || d.LapTimeMs != prev.LapTimeMs {
		t.Errorf("broken event %+v does not match its lap %+v", d, prev)
	}
	if d.PreviousMs != nil {
		t.Errorf("first-PR break must carry no previousMs, got %d", *d.PreviousMs)
	}
	if got[brokenIdx].CorrelationID != got[brokenIdx-1].CorrelationID {
		t.Errorf("broken event correlationId must match the session's")
	}
	// The first observed lap's fields match the first lap.recorded.
	if observed[0].masterID != prev.MasterID {
		// (prev is the first lap since only the first breaks)
		t.Errorf("observed masterId = %q, want %q", observed[0].masterID, prev.MasterID)
	}
}

// No break => no personal_record.broken, but ObservePR still runs per counted lap.
func TestRun_ObservePR_NoBrokenWhenNoBreak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := testConfig(rand.New(rand.NewSource(4)))
	cfg.Drivers = 1
	cfg.SessionLaps = 3
	calls := 0
	cfg.ObservePR = func(context.Context, string, string, int64, string) (bool, *int64, error) {
		calls++
		return false, nil, nil
	}
	broken := 0
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		if e.Type == messaging.PersonalRecordBrokenRoutingKey {
			broken++
		}
		if e.Type == messaging.SessionEndedRoutingKey {
			cancel()
		}
		return nil
	}
	if err := New(cfg).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 3 {
		t.Errorf("ObservePR calls = %d, want 3", calls)
	}
	if broken != 0 {
		t.Errorf("personal_record.broken count = %d, want 0 (no break)", broken)
	}
}

// Regression: with no ObservePR seam wired, Run emits no personal_record.broken and
// behaves exactly as before (the whole PR subsystem is opt-in / simulator-gated).
func TestRun_NoObservePR_NoBrokenEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := testConfig(rand.New(rand.NewSource(5)))
	cfg.Drivers = 1
	broken := 0
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		if e.Type == messaging.PersonalRecordBrokenRoutingKey {
			broken++
		}
		if e.Type == messaging.SessionEndedRoutingKey {
			cancel()
		}
		return nil
	}
	if err := New(cfg).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if broken != 0 {
		t.Errorf("personal_record.broken count = %d, want 0 (no ObservePR wired)", broken)
	}
}

// AC1 "never dropped": an ObservePR failure is fatal to Run — the same escalation tier
// as an Enqueue failure — so a detected PR is never silently lost.
func TestRun_ObservePRErrorIsFatal(t *testing.T) {
	cfg := testConfig(rand.New(rand.NewSource(6)))
	cfg.Drivers = 1
	wantErr := errors.New("pr store closed")
	cfg.ObservePR = func(context.Context, string, string, int64, string) (bool, *int64, error) {
		return false, nil, wantErr
	}
	cfg.Enqueue = func(context.Context, messaging.Envelope) error { return nil }
	err := New(cfg).Run(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want it to wrap %v", err, wantErr)
	}
}
