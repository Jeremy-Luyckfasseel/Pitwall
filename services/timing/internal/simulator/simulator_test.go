package simulator

import (
	"context"
	"encoding/json"
	"math/rand"
	"regexp"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
)

var v4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func marshal(e messaging.Envelope) ([]byte, error) { return json.Marshal(e) }

func testConfig(rng *rand.Rand) Config {
	return Config{
		Drivers:     4,
		LapMeanMs:   45000,
		LapStddevMs: 2000,
		SessionLaps: 5,
		Source:      "timing",
		Rng:         rng,
		Now:         func() time.Time { return time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC) },
	}
}

func validatorForTest(t *testing.T) *messaging.Validator {
	t.Helper()
	dir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("ResolveContractDir: %v", err)
	}
	v, err := messaging.NewValidator(dir)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

// GenerateSession produces a coherent, ordered, contract-valid session:
// session.started first, N*SessionLaps lap.recorded in the middle, session.ended
// last; per-driver lapNumbers run 1..SessionLaps.
func TestGenerateSession_CoherentValidatedStream(t *testing.T) {
	v := validatorForTest(t)
	s := New(testConfig(rand.New(rand.NewSource(1))))
	evs := s.GenerateSession(s.now())

	if len(evs) < 2 {
		t.Fatalf("expected at least started+ended, got %d", len(evs))
	}
	if evs[0].Type != messaging.SessionStartedRoutingKey {
		t.Errorf("first event = %q, want session.started", evs[0].Type)
	}
	if evs[len(evs)-1].Type != messaging.SessionEndedRoutingKey {
		t.Errorf("last event = %q, want session.ended", evs[len(evs)-1].Type)
	}

	lapCount := 0
	perDriverMax := map[string]int{}
	prevAt := time.Time{}
	for _, e := range evs {
		// Every event validates against /contract (producer-half two-sided validation).
		b, err := marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := v.ValidateEnvelopeBytes(b); err != nil {
			t.Fatalf("event %q failed /contract validation: %v", e.Type, err)
		}
		if e.Type == messaging.LapRecordedRoutingKey {
			lapCount++
			d := e.Data.(messaging.LapRecordedData)
			if d.LapTimeMs < 1 {
				t.Errorf("lapTimeMs must be >= 1, got %d", d.LapTimeMs)
			}
			if d.LapNumber > perDriverMax[d.MasterID] {
				perDriverMax[d.MasterID] = d.LapNumber
			}
		}
		// occurredAt is monotonic non-decreasing across the emitted stream.
		got, _ := time.Parse("2006-01-02T15:04:05.000Z", e.OccurredAt)
		if got.Before(prevAt) {
			t.Errorf("stream not time-ordered: %s before %s", e.OccurredAt, prevAt.Format(time.RFC3339Nano))
		}
		prevAt = got
	}

	wantLaps := testConfig(nil).Drivers * testConfig(nil).SessionLaps
	if lapCount != wantLaps {
		t.Errorf("lap.recorded count = %d, want %d (drivers*laps)", lapCount, wantLaps)
	}
	for id, max := range perDriverMax {
		if max != testConfig(nil).SessionLaps {
			t.Errorf("driver %s max lapNumber = %d, want %d", id, max, testConfig(nil).SessionLaps)
		}
	}
}

// Same seed + same base time -> identical data (reproducible demos/tests).
func TestGenerateSession_DeterministicUnderSeed(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	a := New(testConfig(rand.New(rand.NewSource(7)))).GenerateSession(base)
	b := New(testConfig(rand.New(rand.NewSource(7)))).GenerateSession(base)
	if len(a) != len(b) {
		t.Fatalf("different lengths %d vs %d under same seed", len(a), len(b))
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].OccurredAt != b[i].OccurredAt {
			t.Errorf("event %d differs under same seed: %q@%s vs %q@%s", i, a[i].Type, a[i].OccurredAt, b[i].Type, b[i].OccurredAt)
		}
	}
}

// Run emits a full session through the enqueuer in order, then stops cleanly on
// ctx cancel — no sleeps (tick/gap zero; the fake cancels after one session).
func TestRun_EmitsSessionAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var got []messaging.Envelope
	cfg := testConfig(rand.New(rand.NewSource(3)))
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		got = append(got, e)
		if e.Type == messaging.SessionEndedRoutingKey {
			cancel() // one full session captured -> stop
		}
		return nil
	}
	if err := New(cfg).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(got) == 0 || got[0].Type != messaging.SessionStartedRoutingKey {
		t.Fatalf("expected a session.started first, got %d events", len(got))
	}
	if got[len(got)-1].Type != messaging.SessionEndedRoutingKey {
		t.Errorf("expected session.ended last, got %q", got[len(got)-1].Type)
	}
}

// Fixture masterIds are valid lowercase UUID v4 (NOT an identity path).
func TestNew_MintsValidV4FixtureDrivers(t *testing.T) {
	s := New(testConfig(rand.New(rand.NewSource(5))))
	ids := s.DriverIDs()
	if len(ids) != 4 {
		t.Fatalf("expected 4 fixture drivers, got %d", len(ids))
	}
	for _, id := range ids {
		if !v4Pattern.MatchString(id) {
			t.Errorf("driver id %q is not a lowercase UUID v4", id)
		}
	}
}
