package simulator

import (
	"context"
	"log/slog"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
)

// Story 3.6 / AC1 (pure generator): with the out-of-session knob on,
// GenerateReconciledSession produces a self-contained reconciled session for ONE known
// driver — session.started (a DISTINCT "sim-oos-" sessionId), N lap.recorded (numbers
// 1..N), session.ended — all /contract-valid and time-ordered. The crossing is accepted
// as real laps (never dropped, FR83).
func TestGenerateReconciledSession_ProducesReconciledSessionWithLaps(t *testing.T) {
	v := validatorForTest(t)
	base := time.Date(2026, 6, 5, 14, 5, 0, 0, time.UTC)

	cfg := testConfig(rand.New(rand.NewSource(41)))
	cfg.OutOfSessionLaps = 2
	s := prepared(t, cfg)
	knownDriver := s.DriverIDs()[0]

	evs := s.GenerateReconciledSession(base)

	if len(evs) == 0 {
		t.Fatal("GenerateReconciledSession returned no events with the knob on")
	}
	if evs[0].Type != messaging.SessionStartedRoutingKey {
		t.Errorf("first event = %q, want session.started", evs[0].Type)
	}
	if evs[len(evs)-1].Type != messaging.SessionEndedRoutingKey {
		t.Errorf("last event = %q, want session.ended", evs[len(evs)-1].Type)
	}

	started := evs[0].Data.(messaging.SessionStartedData)
	if !strings.HasPrefix(started.SessionID, "sim-oos-") {
		t.Errorf("reconciled sessionId = %q, want a distinct \"sim-oos-\" prefix", started.SessionID)
	}

	laps := 0
	prevAt := time.Time{}
	for _, e := range evs {
		b, err := marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := v.ValidateEnvelopeBytes(b); err != nil {
			t.Fatalf("event %q failed /contract validation: %v", e.Type, err)
		}
		got, _ := time.Parse("2006-01-02T15:04:05.000Z", e.OccurredAt)
		if got.Before(prevAt) {
			t.Errorf("stream not time-ordered: %s before %s", e.OccurredAt, prevAt.Format(time.RFC3339Nano))
		}
		prevAt = got
		if e.Type == messaging.LapRecordedRoutingKey {
			laps++
			d := e.Data.(messaging.LapRecordedData)
			if d.MasterID != knownDriver {
				t.Errorf("reconciled lap masterId = %q, want the known driver %q", d.MasterID, knownDriver)
			}
			if d.SessionID != started.SessionID {
				t.Errorf("reconciled lap sessionId = %q, want %q", d.SessionID, started.SessionID)
			}
			if d.LapNumber != laps {
				t.Errorf("reconciled lapNumber = %d, want %d (sequential)", d.LapNumber, laps)
			}
		}
	}
	if laps != 2 {
		t.Errorf("reconciled lap.recorded count = %d, want 2 (N counted laps from N+1 crossings)", laps)
	}
}

// Regression guard: the default OutOfSessionLaps=0 produces NO reconciled session.
func TestGenerateReconciledSession_ZeroReturnsNil(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 5, 0, 0, time.UTC)
	s := prepared(t, testConfig(rand.New(rand.NewSource(43)))) // OutOfSessionLaps defaults to 0
	if evs := s.GenerateReconciledSession(base); len(evs) != 0 {
		t.Errorf("GenerateReconciledSession returned %d events with the knob off, want 0", len(evs))
	}
}

// Review fix (Blind#2): a reconciled session in which every crossing is rejected by the
// min-lap filter (count == 0) has no real lap to reconcile, so the generator returns nil —
// no session.started/ended is emitted for a phantom out-of-session lap.
func TestGenerateReconciledSession_AllLapsRejectedReturnsNil(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 5, 0, 0, time.UTC)
	cfg := testConfig(rand.New(rand.NewSource(51)))
	cfg.OutOfSessionLaps = 3
	cfg.MinLapTimeMs = 10_000_000 // every reconciled crossing is a sub-min bounce -> rejected
	s := prepared(t, cfg)
	if evs := s.GenerateReconciledSession(base); len(evs) != 0 {
		t.Errorf("GenerateReconciledSession returned %d events when all laps are rejected, want 0 (nothing to reconcile)", len(evs))
	}
}

// Review fix (Blind#1): the session-active flag gates the corrective emission, not merely the
// WARN log — if a session is (nominally) active, emitReconciledSession is a no-op (there is
// nothing out-of-session to reconcile).
func TestEmitReconciledSession_SkippedWhenSessionActive(t *testing.T) {
	enqueued := 0
	cfg := testConfig(rand.New(rand.NewSource(49)))
	cfg.OutOfSessionLaps = 2
	cfg.Enqueue = func(context.Context, messaging.Envelope) error { enqueued++; return nil }
	s := prepared(t, cfg)
	st := &runState{active: true} // a session is (nominally) already active
	if err := s.emitReconciledSession(context.Background(), st); err != nil {
		t.Fatalf("emitReconciledSession error: %v", err)
	}
	if enqueued != 0 {
		t.Errorf("emitReconciledSession enqueued %d events while a session was active, want 0", enqueued)
	}
}

// Story 3.6 / AC1+AC2 (Run): with the knob on, Run auto-starts a reconciled out-of-session
// session during the inter-session gap — enqueuing a DISTINCT session.started + N
// lap.recorded + session.ended — and logs the reconcile at WARN severity (NOT an alert).
func TestRun_OutOfSession_AutoStartsAndWarns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logs := &logCapture{}
	var got []messaging.Envelope
	sessionEnds := 0
	cfg := testConfig(rand.New(rand.NewSource(45)))
	cfg.OutOfSessionLaps = 2
	cfg.Log = slog.New(logs)
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		got = append(got, e)
		if e.Type == messaging.SessionEndedRoutingKey {
			sessionEnds++
			if sessionEnds >= 2 { // stop after the reconciled session ends (2nd session.ended)
				cancel()
			}
		}
		return nil
	}
	if err := New(cfg).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Exactly one reconciled ("sim-oos-") session.started + session.ended, carrying laps.
	var oosStart, oosEnd, oosLaps int
	var oosSessionID string
	for _, e := range got {
		switch e.Type {
		case messaging.SessionStartedRoutingKey:
			if d := e.Data.(messaging.SessionStartedData); strings.HasPrefix(d.SessionID, "sim-oos-") {
				oosStart++
				oosSessionID = d.SessionID
			}
		case messaging.SessionEndedRoutingKey:
			if d := e.Data.(messaging.SessionEndedData); strings.HasPrefix(d.SessionID, "sim-oos-") {
				oosEnd++
			}
		case messaging.LapRecordedRoutingKey:
			if d := e.Data.(messaging.LapRecordedData); d.SessionID == oosSessionID && oosSessionID != "" {
				oosLaps++
			}
		}
	}
	if oosStart != 1 || oosEnd != 1 {
		t.Errorf("reconciled session.started/ended = %d/%d, want 1/1", oosStart, oosEnd)
	}
	if oosLaps != 2 {
		t.Errorf("reconciled lap.recorded count = %d, want 2 (accepted, never dropped)", oosLaps)
	}

	rec, ok := logs.find("out-of-session crossing — reconciling session state (physical reality wins)")
	if !ok {
		t.Fatalf("expected the out-of-session reconcile WARN log line, got %+v", logs.records)
	}
	if rec.level != slog.LevelWarn {
		t.Errorf("reconcile log level = %v, want Warn (FR83 says 'log a warning', not alert)", rec.level)
	}
	if rec.attrs["reconcile"] != "out_of_session_lap" {
		t.Errorf("reconcile log attr = %q, want %q", rec.attrs["reconcile"], "out_of_session_lap")
	}
	if _, isAlert := rec.attrs["alert"]; isAlert {
		t.Errorf("reconcile log must NOT carry an \"alert\" attribute (it is a WARN, not an operator alert)")
	}
}

// Review fix (Edge#1): the reconciled out-of-session session models a crossing during the
// inter-session GAP, so its events must not PRECEDE the normal session that just ended — Run
// bases the reconciled session at that session's end time. Assert the reconciled
// session.started occurredAt is >= the normal session.ended occurredAt (wire timestamps sort
// lexicographically).
func TestRun_OutOfSession_ReconciledSessionFollowsNormalSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var got []messaging.Envelope
	sessionEnds := 0
	cfg := testConfig(rand.New(rand.NewSource(55)))
	cfg.OutOfSessionLaps = 2
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		got = append(got, e)
		if e.Type == messaging.SessionEndedRoutingKey {
			sessionEnds++
			if sessionEnds >= 2 {
				cancel()
			}
		}
		return nil
	}
	if err := New(cfg).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var normalEndedAt, oosStartedAt string
	for _, e := range got {
		switch e.Type {
		case messaging.SessionEndedRoutingKey:
			d := e.Data.(messaging.SessionEndedData)
			if !strings.HasPrefix(d.SessionID, "sim-oos-") {
				normalEndedAt = e.OccurredAt
			}
		case messaging.SessionStartedRoutingKey:
			d := e.Data.(messaging.SessionStartedData)
			if strings.HasPrefix(d.SessionID, "sim-oos-") {
				oosStartedAt = e.OccurredAt
			}
		}
	}
	if normalEndedAt == "" || oosStartedAt == "" {
		t.Fatalf("missing timestamps: normalEndedAt=%q oosStartedAt=%q", normalEndedAt, oosStartedAt)
	}
	if oosStartedAt < normalEndedAt {
		t.Errorf("reconciled session.started (%s) precedes the normal session.ended (%s) — the out-of-session gap must come after", oosStartedAt, normalEndedAt)
	}
}

// Regression guard: with OutOfSessionLaps=0, Run emits NO reconciled session and NO
// reconcile WARN — behavior is unchanged from before Story 3.6.
func TestRun_NoOutOfSessionByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logs := &logCapture{}
	var oosSessions int
	cfg := testConfig(rand.New(rand.NewSource(47)))
	cfg.Log = slog.New(logs)
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		if e.Type == messaging.SessionStartedRoutingKey {
			if d := e.Data.(messaging.SessionStartedData); strings.HasPrefix(d.SessionID, "sim-oos-") {
				oosSessions++
			}
		}
		if e.Type == messaging.SessionEndedRoutingKey {
			cancel()
		}
		return nil
	}
	if err := New(cfg).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if oosSessions != 0 {
		t.Errorf("reconciled sessions = %d with the knob off, want 0", oosSessions)
	}
	if _, ok := logs.find("out-of-session crossing — reconciling session state (physical reality wins)"); ok {
		t.Errorf("unexpected reconcile WARN with OutOfSessionLaps=0")
	}
}
