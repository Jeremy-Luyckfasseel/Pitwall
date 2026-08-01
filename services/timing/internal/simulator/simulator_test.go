package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
)

// logCapture is a minimal slog.Handler that records emitted log lines for assertion —
// this package has no existing log-capture helper, so Story 2.4 adds this narrow one to
// prove Prepare logs a hand-out vs a reassignment (AC1/AC2), rather than pulling in a
// third-party test-logging dependency for two assertions.
type logCapture struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, capturedRecord{level: r.Level, msg: r.Message, attrs: attrs})
	c.mu.Unlock()
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) find(msg string) (capturedRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.msg == msg {
			return r, true
		}
	}
	return capturedRecord{}, false
}

var v4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func marshal(e messaging.Envelope) ([]byte, error) { return json.Marshal(e) }

// fakeResolve is a deterministic stand-in for the Identity request/reply: it maps an
// email to a stable lowercase UUID v4 (so the same email always resolves to the same
// id, exactly like the real idempotent Identity). The real bus resolver is wired in
// cmd/timing (Task 6) + exercised by the integration/conformance layers.
func fakeResolve(_ context.Context, email string) (string, error) {
	h := fnv.New128a()
	_, _ = h.Write([]byte(email))
	var b [16]byte
	copy(b[:], h.Sum(nil))
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10x
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func testConfig(rng *rand.Rand) Config {
	return Config{
		Drivers:      4,
		Transponders: 0, // default: all QR (the smoke's shape); >0 exercises the map path
		LapMeanMs:    45000,
		LapStddevMs:  2000,
		SessionLaps:  5,
		MinLapTimeMs: 10000, // filter active, but well below the mean -> rejects nothing
		Source:       "timing",
		Rng:          rng,
		Now:          func() time.Time { return time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC) },
		Resolve:      fakeResolve,
		AssignTransponder: func(context.Context, string, string) (bool, string, error) {
			return false, "", nil
		},
		RecordHeldScan: func(context.Context, string, string, string, string, string) error {
			return nil
		},
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

func prepared(t *testing.T, cfg Config) *Simulator {
	t.Helper()
	s := New(cfg)
	if err := s.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return s
}

// GenerateSession produces a coherent, ordered, contract-valid session:
// session.started first, one driver.checked_in per driver at the gate, then
// N*SessionLaps lap.recorded, session.ended last; per-driver lapNumbers run
// 1..SessionLaps. Every event uses the REAL (resolved) masterId, never a fixture.
func TestGenerateSession_CoherentValidatedStream(t *testing.T) {
	v := validatorForTest(t)
	s := prepared(t, testConfig(rand.New(rand.NewSource(1))))
	evs, held := s.GenerateSession(s.now())
	if len(held) != 0 {
		t.Fatalf("held scans = %d, want 0 (UnknownTokenScans defaults to 0)", len(held))
	}

	if evs[0].Type != messaging.SessionStartedRoutingKey {
		t.Errorf("first event = %q, want session.started", evs[0].Type)
	}
	if evs[len(evs)-1].Type != messaging.SessionEndedRoutingKey {
		t.Errorf("last event = %q, want session.ended", evs[len(evs)-1].Type)
	}

	lapCount, checkInCount := 0, 0
	sawFirstLap := false
	perDriverMax := map[string]int{}
	prevAt := time.Time{}
	for _, e := range evs {
		b, err := marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := v.ValidateEnvelopeBytes(b); err != nil {
			t.Fatalf("event %q failed /contract validation: %v", e.Type, err)
		}
		switch e.Type {
		case messaging.DriverCheckedInRoutingKey:
			checkInCount++
			if sawFirstLap {
				t.Errorf("driver.checked_in emitted AFTER a lap.recorded — check-in must precede laps")
			}
			d := e.Data.(messaging.CheckedInData)
			if !v4Pattern.MatchString(d.MasterID) {
				t.Errorf("checked_in masterId %q is not a resolved v4", d.MasterID)
			}
			if d.CheckInMethod != messaging.CheckInMethodQR && d.CheckInMethod != messaging.CheckInMethodTransponder {
				t.Errorf("unexpected checkInMethod %q", d.CheckInMethod)
			}
		case messaging.LapRecordedRoutingKey:
			sawFirstLap = true
			lapCount++
			d := e.Data.(messaging.LapRecordedData)
			if d.LapTimeMs < 1 {
				t.Errorf("lapTimeMs must be >= 1, got %d", d.LapTimeMs)
			}
			if d.LapNumber > perDriverMax[d.MasterID] {
				perDriverMax[d.MasterID] = d.LapNumber
			}
		}
		got, _ := time.Parse("2006-01-02T15:04:05.000Z", e.OccurredAt)
		if got.Before(prevAt) {
			t.Errorf("stream not time-ordered: %s before %s", e.OccurredAt, prevAt.Format(time.RFC3339Nano))
		}
		prevAt = got
	}

	if checkInCount != testConfig(nil).Drivers {
		t.Errorf("driver.checked_in count = %d, want %d (one per driver)", checkInCount, testConfig(nil).Drivers)
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

// A transponder driver checks in with checkInMethod:"transponder" + its hardware id,
// and its mapping is assigned (so the gate resolves it) — Prepare calls the hand-out
// trigger AssignTransponder with the resolved masterId. QR drivers check in with
// checkInMethod:"qr", null hw id.
func TestPrepareAndGenerate_TransponderDriverCheckedInAndAssigned(t *testing.T) {
	cfg := testConfig(rand.New(rand.NewSource(2)))
	cfg.Drivers = 3
	cfg.Transponders = 1 // the first driver uses a transponder; the other two QR

	type seed struct{ tp, master string }
	var seeds []seed
	cfg.AssignTransponder = func(_ context.Context, tp, master string) (bool, string, error) {
		seeds = append(seeds, seed{tp, master})
		return false, "", nil
	}

	s := prepared(t, cfg)
	evs, _ := s.GenerateSession(s.now())

	if len(seeds) != 1 {
		t.Fatalf("expected exactly 1 transponder mapping seeded, got %d", len(seeds))
	}
	if !v4Pattern.MatchString(seeds[0].master) {
		t.Errorf("seeded masterId %q is not a resolved v4", seeds[0].master)
	}

	var qr, tp int
	for _, e := range evs {
		if e.Type != messaging.DriverCheckedInRoutingKey {
			continue
		}
		d := e.Data.(messaging.CheckedInData)
		switch d.CheckInMethod {
		case messaging.CheckInMethodTransponder:
			tp++
			if d.TransponderID == nil || *d.TransponderID == "" {
				t.Errorf("transponder check-in must carry a hardware id, got %v", d.TransponderID)
			}
			if d.TransponderID != nil && *d.TransponderID != seeds[0].tp {
				t.Errorf("check-in hw id %q != seeded %q", *d.TransponderID, seeds[0].tp)
			}
			if d.MasterID != seeds[0].master {
				t.Errorf("transponder check-in masterId %q != seeded %q", d.MasterID, seeds[0].master)
			}
		case messaging.CheckInMethodQR:
			qr++
			if d.TransponderID != nil {
				t.Errorf("QR check-in must have null transponderId, got %v", *d.TransponderID)
			}
		}
	}
	if tp != 1 || qr != 2 {
		t.Errorf("check-in method split = %d transponder / %d qr, want 1/2", tp, qr)
	}
}

// AC4: with the min-lap filter active on clean (above-threshold) input the simulator's
// output is identical to the unfiltered case — same seed + base -> same stream.
func TestGenerateSession_FilterActiveLeavesCleanStreamUnchanged(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)

	off := testConfig(rand.New(rand.NewSource(11)))
	off.MinLapTimeMs = 0
	a, _ := prepared(t, off).GenerateSession(base)

	on := testConfig(rand.New(rand.NewSource(11)))
	on.MinLapTimeMs = 10000
	b, _ := prepared(t, on).GenerateSession(base)

	if len(a) != len(b) {
		t.Fatalf("filter changed the stream length: off=%d on=%d", len(a), len(b))
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].OccurredAt != b[i].OccurredAt {
			t.Errorf("event %d type/time differs with the filter on: %q@%s vs %q@%s",
				i, a[i].Type, a[i].OccurredAt, b[i].Type, b[i].OccurredAt)
		}
		if !reflect.DeepEqual(a[i].Data, b[i].Data) {
			t.Errorf("event %d data differs with the filter on: %+v vs %+v", i, a[i].Data, b[i].Data)
		}
	}
}

// Same seed + same base time -> identical data (reproducible demos/tests).
func TestGenerateSession_DeterministicUnderSeed(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	a, _ := prepared(t, testConfig(rand.New(rand.NewSource(7)))).GenerateSession(base)
	b, _ := prepared(t, testConfig(rand.New(rand.NewSource(7)))).GenerateSession(base)
	if len(a) != len(b) {
		t.Fatalf("different lengths %d vs %d under same seed", len(a), len(b))
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].OccurredAt != b[i].OccurredAt {
			t.Errorf("event %d differs under same seed: %q@%s vs %q@%s", i, a[i].Type, a[i].OccurredAt, b[i].Type, b[i].OccurredAt)
		}
		if !reflect.DeepEqual(a[i].Data, b[i].Data) {
			t.Errorf("event %d data differs under same seed", i)
		}
	}
}

// Story 2.5, AC1 (regression guard): the default UnknownTokenScans=0 produces ZERO
// held scans and leaves the envelope stream byte-identical to pre-2.5 behavior — the
// register-first gate must not change anything when there is nothing to hold.
func TestGenerateSession_UnknownTokenScansZero_NoHeldScans(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	cfg := testConfig(rand.New(rand.NewSource(9)))
	evs, held := prepared(t, cfg).GenerateSession(base)

	if len(held) != 0 {
		t.Fatalf("held scans = %d, want 0 (UnknownTokenScans defaults to 0)", len(held))
	}
	wantLaps := cfg.Drivers * cfg.SessionLaps
	lapCount := 0
	for _, e := range evs {
		if e.Type == messaging.LapRecordedRoutingKey {
			lapCount++
		}
	}
	if lapCount != wantLaps {
		t.Errorf("lap.recorded count = %d, want %d (drivers*laps, unchanged)", lapCount, wantLaps)
	}
}

// Story 2.5, AC1/AC2: a token with no completed check-in this session (a stray
// transponder) is HELD, not counted — it never reaches a LapTracker, never produces a
// lap.recorded, and is reported back for the caller (Run) to persist + flag. Real
// drivers' laps are unaffected (no cross-contamination between the gate and the
// existing min-lap-time filter).
func TestGenerateSession_UnknownTokenScans_HeldNotCounted(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)

	baseline := testConfig(rand.New(rand.NewSource(9)))
	baselineEvs, _ := prepared(t, baseline).GenerateSession(base)

	cfg := testConfig(rand.New(rand.NewSource(9)))
	cfg.UnknownTokenScans = 1
	evs, held := prepared(t, cfg).GenerateSession(base)

	if len(held) != 1 {
		t.Fatalf("held scans = %d, want 1", len(held))
	}
	h := held[0]
	if h.Token == "" {
		t.Error("held scan Token is empty")
	}
	if h.Method != messaging.CheckInMethodTransponder {
		t.Errorf("held scan Method = %q, want %q", h.Method, messaging.CheckInMethodTransponder)
	}
	if h.SessionID == "" {
		t.Error("held scan SessionID is empty")
	}
	if h.Reason == "" {
		t.Error("held scan Reason is empty")
	}
	if h.At.IsZero() {
		t.Error("held scan At is zero")
	}

	for _, e := range evs {
		if e.Type != messaging.LapRecordedRoutingKey {
			continue
		}
		d := e.Data.(messaging.LapRecordedData)
		if d.MasterID == h.Token {
			t.Errorf("lap.recorded emitted for the held/stray token %q — must never be counted", h.Token)
		}
	}

	// Real drivers' events are unaffected by the injected stray (no cross-contamination).
	realOnly := make([]messaging.Envelope, 0, len(evs))
	for _, e := range evs {
		if e.Type == messaging.LapRecordedRoutingKey && e.Data.(messaging.LapRecordedData).MasterID == h.Token {
			continue
		}
		realOnly = append(realOnly, e)
	}
	if len(realOnly) != len(baselineEvs) {
		t.Fatalf("real-driver event count = %d, want %d (baseline, unaffected by the stray)", len(realOnly), len(baselineEvs))
	}
	for i := range realOnly {
		if realOnly[i].Type != baselineEvs[i].Type || realOnly[i].OccurredAt != baselineEvs[i].OccurredAt {
			t.Errorf("event %d differs from baseline: %q@%s vs %q@%s",
				i, realOnly[i].Type, realOnly[i].OccurredAt, baselineEvs[i].Type, baselineEvs[i].OccurredAt)
		}
	}
}

// Story 2.5 (review follow-up, Blind#6): UnknownTokenScans > 1 injects that many
// DISTINCT stray tokens, each held (never counted), not just the N=1 case the other
// tests exercise.
func TestGenerateSession_UnknownTokenScans_MultipleStraysAllHeld(t *testing.T) {
	base := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	cfg := testConfig(rand.New(rand.NewSource(17)))
	cfg.UnknownTokenScans = 3
	evs, held := prepared(t, cfg).GenerateSession(base)

	if len(held) != 3 {
		t.Fatalf("held scans = %d, want 3", len(held))
	}
	seen := map[string]bool{}
	for _, h := range held {
		if seen[h.Token] {
			t.Errorf("duplicate held token %q, want 3 distinct strays", h.Token)
		}
		seen[h.Token] = true
		if h.Method != messaging.CheckInMethodTransponder {
			t.Errorf("held scan Method = %q, want %q", h.Method, messaging.CheckInMethodTransponder)
		}
	}
	if len(seen) != 3 {
		t.Errorf("distinct held tokens = %d, want 3", len(seen))
	}

	for _, e := range evs {
		if e.Type != messaging.LapRecordedRoutingKey {
			continue
		}
		d := e.Data.(messaging.LapRecordedData)
		if seen[d.MasterID] {
			t.Errorf("lap.recorded emitted for held/stray token %q — must never be counted", d.MasterID)
		}
	}
}

// Run prepares (resolves ids) then emits a full session through the enqueuer in order,
// then stops cleanly on ctx cancel — no sleeps (tick/gap zero).
func TestRun_EmitsSessionAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var got []messaging.Envelope
	cfg := testConfig(rand.New(rand.NewSource(3)))
	cfg.Enqueue = func(_ context.Context, e messaging.Envelope) error {
		got = append(got, e)
		if e.Type == messaging.SessionEndedRoutingKey {
			cancel()
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

// Story 2.5 (AC2): a held scan is persisted via RecordHeldScan and logged at alert
// severity exactly once per UnknownTokenScans.
func TestRun_HeldScanRecordedAndLogged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logs := &logCapture{}
	var recorded []string
	cfg := testConfig(rand.New(rand.NewSource(13)))
	cfg.UnknownTokenScans = 1
	cfg.Log = slog.New(logs)
	cfg.RecordHeldScan = func(_ context.Context, token, method, sessionID, occurredAt, reason string) error {
		recorded = append(recorded, token)
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
	if len(recorded) != 1 {
		t.Fatalf("RecordHeldScan calls = %d, want 1", len(recorded))
	}
	rec, ok := logs.find("unknown token at line — held for operator late-binding")
	if !ok {
		t.Fatalf("expected the unknown-token alert log line, got %+v", logs.records)
	}
	if rec.level != slog.LevelError {
		t.Errorf("held-scan log level = %v, want Error (alert severity)", rec.level)
	}
	if rec.attrs["alert"] != "unknown_token_at_line" {
		t.Errorf("held-scan log alert attr = %q, want %q", rec.attrs["alert"], "unknown_token_at_line")
	}
	if rec.attrs["token"] != recorded[0] {
		t.Errorf("held-scan log token = %q, want %q", rec.attrs["token"], recorded[0])
	}
}

// Story 2.5 (AC1 regression guard): the default UnknownTokenScans=0 neither records nor
// logs a held scan.
func TestRun_NoHeldScanByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logs := &logCapture{}
	var recordCalls int
	cfg := testConfig(rand.New(rand.NewSource(14)))
	cfg.Log = slog.New(logs)
	cfg.RecordHeldScan = func(context.Context, string, string, string, string, string) error {
		recordCalls++
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
	if recordCalls != 0 {
		t.Errorf("RecordHeldScan calls = %d, want 0", recordCalls)
	}
	if _, ok := logs.find("unknown token at line — held for operator late-binding"); ok {
		t.Errorf("unexpected unknown-token alert log with UnknownTokenScans=0")
	}
}

// Story 2.5 (AC2 "never dropped"): a RecordHeldScan failure is fatal to Run — the same
// escalation tier as an Enqueue failure — so a held scan is never silently lost.
func TestRun_RecordHeldScanErrorIsFatal(t *testing.T) {
	cfg := testConfig(rand.New(rand.NewSource(15)))
	cfg.UnknownTokenScans = 1
	wantErr := errors.New("db closed")
	cfg.RecordHeldScan = func(context.Context, string, string, string, string, string) error {
		return wantErr
	}
	err := New(cfg).Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil error, want a fatal error from RecordHeldScan")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Run error = %v, want it to wrap %v", err, wantErr)
	}
}

// A first-time hand-out (AssignTransponder returns reassigned=false) logs a plain
// "transponder handed out" info line, and must NOT log a reassignment (AC1).
func TestPrepare_LogsTransponderHandOut(t *testing.T) {
	logs := &logCapture{}
	cfg := testConfig(rand.New(rand.NewSource(9)))
	cfg.Drivers, cfg.Transponders = 1, 1
	cfg.Log = slog.New(logs)
	cfg.AssignTransponder = func(context.Context, string, string) (bool, string, error) {
		return false, "", nil
	}
	prepared(t, cfg)

	rec, ok := logs.find("transponder handed out")
	if !ok {
		t.Fatalf("expected a %q log line, got %+v", "transponder handed out", logs.records)
	}
	if rec.level != slog.LevelInfo {
		t.Errorf("hand-out log level = %v, want Info", rec.level)
	}
	if rec.attrs["transponderId"] == "" || rec.attrs["masterId"] == "" {
		t.Errorf("hand-out log missing transponderId/masterId attrs: %+v", rec.attrs)
	}
	if _, reassignLogged := logs.find("transponder reassigned"); reassignLogged {
		t.Errorf("first-time hand-out must not log a reassignment")
	}
}

// A reassignment (AssignTransponder returns reassigned=true) logs a "transponder
// reassigned" warn line carrying the previous and new masterId (AC2).
func TestPrepare_LogsTransponderReassignment(t *testing.T) {
	logs := &logCapture{}
	cfg := testConfig(rand.New(rand.NewSource(10)))
	cfg.Drivers, cfg.Transponders = 1, 1
	cfg.Log = slog.New(logs)
	const previous = "9f8e7d6c-5b4a-4321-8765-0a1b2c3d4e5f"
	cfg.AssignTransponder = func(context.Context, string, string) (bool, string, error) {
		return true, previous, nil
	}
	prepared(t, cfg)

	rec, ok := logs.find("transponder reassigned")
	if !ok {
		t.Fatalf("expected a %q log line, got %+v", "transponder reassigned", logs.records)
	}
	if rec.level != slog.LevelWarn {
		t.Errorf("reassignment log level = %v, want Warn", rec.level)
	}
	if rec.attrs["previousMasterId"] != previous {
		t.Errorf("reassignment log previousMasterId = %q, want %q", rec.attrs["previousMasterId"], previous)
	}
	if rec.attrs["transponderId"] == "" || rec.attrs["masterId"] == "" {
		t.Errorf("reassignment log missing transponderId/masterId attrs: %+v", rec.attrs)
	}
}

// Register-first: each driver carries a deterministic email and, after Prepare,
// a REAL resolved masterId (valid v4) — fixtures are gone (Q&A Round 32 / Q32.1).
func TestPrepare_ResolvesRealMasterIDsPerDriver(t *testing.T) {
	s := prepared(t, testConfig(rand.New(rand.NewSource(5))))
	ids := s.DriverIDs()
	if len(ids) != 4 {
		t.Fatalf("expected 4 drivers, got %d", len(ids))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !v4Pattern.MatchString(id) {
			t.Errorf("driver id %q is not a resolved lowercase UUID v4", id)
		}
		seen[id] = true
	}
	if len(seen) != 4 {
		t.Errorf("expected 4 distinct resolved masterIds, got %d", len(seen))
	}
}
