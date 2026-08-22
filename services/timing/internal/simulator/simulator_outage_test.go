package simulator

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
)

// countType counts events of a given routing key in a stream.
func countType(evs []messaging.Envelope, typ string) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// Story 3.5 / AC1+AC2: with the outage knob on, GenerateSession drops a contiguous run of
// crossings (the honest gap — fewer laps than the same seed with the knob off) and emits
// exactly one scanner.offline and one scanner.online, all events /contract-valid and time-
// ordered.
func TestGenerateSession_ScannerOutage_DropsCrossingsAndEmitsEvents(t *testing.T) {
	v := validatorForTest(t)
	base := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)

	baseline, _ := prepared(t, testConfig(rand.New(rand.NewSource(21)))).GenerateSession(base)
	baselineLaps := countType(baseline, messaging.LapRecordedRoutingKey)

	cfg := testConfig(rand.New(rand.NewSource(21)))
	cfg.ScannerOutageLaps = 2 // SessionLaps is 5
	evs, _ := prepared(t, cfg).GenerateSession(base)

	if got := countType(evs, messaging.ScannerOfflineRoutingKey); got != 1 {
		t.Errorf("scanner.offline count = %d, want 1", got)
	}
	if got := countType(evs, messaging.ScannerOnlineRoutingKey); got != 1 {
		t.Errorf("scanner.online count = %d, want 1", got)
	}
	laps := countType(evs, messaging.LapRecordedRoutingKey)
	if laps >= baselineLaps {
		t.Errorf("outage lap count = %d, want fewer than baseline %d (the honest gap)", laps, baselineLaps)
	}
	if laps == 0 {
		t.Errorf("outage lap count = 0, want laps before and after the gap")
	}

	// Every event validates, and the stream stays time-ordered (scanner.offline sits between
	// the pre-gap and post-gap laps; scanner.online just before the resume crossing).
	prevAt := time.Time{}
	var offlineAt, onlineAt string
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
		switch e.Type {
		case messaging.ScannerOfflineRoutingKey:
			d := e.Data.(messaging.ScannerOfflineData)
			if d.ScannerID != messaging.ScannerID {
				t.Errorf("scanner.offline scannerId = %q, want %q", d.ScannerID, messaging.ScannerID)
			}
			if d.GapFrom > d.Since {
				t.Errorf("gapFrom %q should be <= since %q (last good crossing before the gap)", d.GapFrom, d.Since)
			}
			offlineAt = e.OccurredAt
		case messaging.ScannerOnlineRoutingKey:
			d := e.Data.(messaging.ScannerOnlineData)
			if d.ScannerID != messaging.ScannerID {
				t.Errorf("scanner.online scannerId = %q, want %q", d.ScannerID, messaging.ScannerID)
			}
			onlineAt = e.OccurredAt
		}
	}
	if offlineAt == "" || onlineAt == "" {
		t.Fatalf("expected both scanner.offline and scanner.online in the stream")
	}
	if onlineAt <= offlineAt {
		t.Errorf("scanner.online (%s) should occur after scanner.offline (%s)", onlineAt, offlineAt)
	}
}

// Story 3.5 / C1 "never faked": no counted lap spans the outage. After recovery the affected
// drivers' trackers are Reset, so the resume crossing is a fresh out-lap and no lap.recorded
// carries an inflated time covering the ~2-lap gap (gap ≈ 2*45s = 90s; normal lap ≈ 45s).
func TestGenerateSession_ScannerOutage_NoGapSpanningLap(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	cfg := testConfig(rand.New(rand.NewSource(23)))
	cfg.ScannerOutageLaps = 2
	evs, _ := prepared(t, cfg).GenerateSession(base)

	const sane = int64(100000) // well above a normal ~45s lap, well below the ~90s gap
	for _, e := range evs {
		if e.Type != messaging.LapRecordedRoutingKey {
			continue
		}
		d := e.Data.(messaging.LapRecordedData)
		if d.LapTimeMs >= sane {
			t.Errorf("lap.recorded lapTimeMs = %d spans the outage gap (faked); want a normally-timed lap < %d", d.LapTimeMs, sane)
		}
	}
}

// Regression guard: the default ScannerOutageLaps=0 emits NO scanner events and leaves the
// stream byte-identical to the pre-3.5 baseline (same seed).
func TestGenerateSession_ScannerOutageZero_NoScannerEventsUnchangedStream(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	baseline, _ := prepared(t, testConfig(rand.New(rand.NewSource(25)))).GenerateSession(base)

	cfg := testConfig(rand.New(rand.NewSource(25))) // ScannerOutageLaps defaults to 0
	evs, _ := prepared(t, cfg).GenerateSession(base)

	if got := countType(evs, messaging.ScannerOfflineRoutingKey) + countType(evs, messaging.ScannerOnlineRoutingKey); got != 0 {
		t.Errorf("scanner event count = %d with outage off, want 0", got)
	}
	if len(evs) != len(baseline) {
		t.Fatalf("event count = %d, want %d (unchanged with outage off)", len(evs), len(baseline))
	}
	for i := range evs {
		if evs[i].Type != baseline[i].Type || evs[i].OccurredAt != baseline[i].OccurredAt {
			t.Errorf("event %d differs from baseline: %q@%s vs %q@%s", i, evs[i].Type, evs[i].OccurredAt, baseline[i].Type, baseline[i].OccurredAt)
		}
	}
}

// Story 3.5 / AC1+AC2: Run persists the outage (OpenOutage then CloseOutage on the same id)
// and logs the Control-Room alert (Error severity, alert=scanner_offline) + the recovery.
func TestRun_ScannerOutage_RecordsAndLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logs := &logCapture{}
	var openCalls, closeCalls int
	var openedID, closedID int64
	cfg := testConfig(rand.New(rand.NewSource(27)))
	cfg.ScannerOutageLaps = 2
	cfg.Log = slog.New(logs)
	cfg.OpenOutage = func(_ context.Context, scannerID, sessionID, gapFrom, since, recordedAt string) (int64, error) {
		openCalls++
		openedID = 42
		return openedID, nil
	}
	cfg.CloseOutage = func(_ context.Context, id int64, onlineAt string) error {
		closeCalls++
		closedID = id
		return nil
	}
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		if e.Type == messaging.SessionEndedRoutingKey {
			cancel()
		}
		return nil
	}
	if err := New(cfg).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if openCalls != 1 {
		t.Fatalf("OpenOutage calls = %d, want 1", openCalls)
	}
	if closeCalls != 1 {
		t.Fatalf("CloseOutage calls = %d, want 1", closeCalls)
	}
	if closedID != openedID {
		t.Errorf("CloseOutage id = %d, want the OpenOutage id %d", closedID, openedID)
	}
	rec, ok := logs.find("scanner offline — start-finish silent, gap flagged (persist-first; missed crossings never faked)")
	if !ok {
		t.Fatalf("expected the scanner-offline alert log line, got %+v", logs.records)
	}
	if rec.level != slog.LevelError {
		t.Errorf("scanner-offline log level = %v, want Error (alert severity)", rec.level)
	}
	if rec.attrs["alert"] != "scanner_offline" {
		t.Errorf("scanner-offline log alert attr = %q, want %q", rec.attrs["alert"], "scanner_offline")
	}
	if _, ok := logs.find("scanner online — start-finish recovered"); !ok {
		t.Errorf("expected a scanner-online recovery log line")
	}
}

// Regression guard: with ScannerOutageLaps=0, Run neither records nor emits a scanner event.
func TestRun_NoScannerOutageByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var openCalls int
	var scannerEvents int
	cfg := testConfig(rand.New(rand.NewSource(29)))
	cfg.OpenOutage = func(context.Context, string, string, string, string, string) (int64, error) {
		openCalls++
		return 1, nil
	}
	cfg.CloseOutage = func(context.Context, int64, string) error { return nil }
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		if e.Type == messaging.ScannerOfflineRoutingKey || e.Type == messaging.ScannerOnlineRoutingKey {
			scannerEvents++
		}
		if e.Type == messaging.SessionEndedRoutingKey {
			cancel()
		}
		return nil
	}
	if err := New(cfg).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if openCalls != 0 {
		t.Errorf("OpenOutage calls = %d, want 0 (outage off)", openCalls)
	}
	if scannerEvents != 0 {
		t.Errorf("scanner events enqueued = %d, want 0 (outage off)", scannerEvents)
	}
}

// Story 3.5 ("never dropped"): an OpenOutage failure is fatal to Run — the same escalation
// tier as an Enqueue/RecordHeldScan failure — so a flagged gap is never silently lost.
func TestRun_ScannerOutage_OpenOutageErrorIsFatal(t *testing.T) {
	cfg := testConfig(rand.New(rand.NewSource(31)))
	cfg.ScannerOutageLaps = 2
	wantErr := errors.New("db closed")
	cfg.OpenOutage = func(context.Context, string, string, string, string, string) (int64, error) {
		return 0, wantErr
	}
	cfg.CloseOutage = func(context.Context, int64, string) error { return nil }
	cfg.Enqueue = func(context.Context, messaging.Envelope) error { return nil }
	err := New(cfg).Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil error, want a fatal error from OpenOutage")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Run error = %v, want it to wrap %v", err, wantErr)
	}
}
