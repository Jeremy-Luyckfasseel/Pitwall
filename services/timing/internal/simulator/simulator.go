// Package simulator is Timing's development simulator (FR40): with no hardware,
// it generates a realistic stream of start-finish crossings for N fixture
// drivers and emits the platform's three core domain events — session.started,
// lap.recorded (one per counted lap), session.ended — through the Story-1.4
// transactional outbox. It runs continuous sessions so the whole platform has
// ongoing end-to-end activity in dev and demos.
//
// The N drivers carry FIXTURE masterIds minted locally (valid UUID v4). These
// are NOT an identity path — real Identity-issued ids replace them in Epic 2; no
// id-minting path is baked into the skeleton.
//
// Determinism: generation is driven by an injected *rand.Rand and a virtual
// clock, so a seed reproduces a session exactly (used by tests and reproducible
// demos). The minimum-lap-time bounce filter is NOT here — that is Story 1.6;
// this simulator only generates clean, above-threshold laps.
package simulator

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
)

// Config wires the simulator's parameters and dependencies.
type Config struct {
	Drivers     int           // N fixture drivers (>= 1)
	LapMeanMs   int           // normal-distribution mean lap time
	LapStddevMs int           // normal-distribution stddev
	SessionLaps int           // counted laps per driver before the session ends
	Tick        time.Duration // wall-clock pacing between emitted events (0 = no pacing)
	SessionGap  time.Duration // pause between sessions in the continuous loop
	Source      string        // envelope `source` (== service name, "timing")
	Rng         *rand.Rand    // injected RNG (deterministic under a seed)
	Now         func() time.Time
	Enqueue     func(ctx context.Context, env messaging.Envelope) error // outbox seam
	Log         *slog.Logger
}

// Simulator generates and emits simulated sessions. Construct it with New.
type Simulator struct {
	cfg     Config
	drivers []string // fixture masterIds, stable for this simulator's lifetime
}

// New builds a simulator and mints its N fixture drivers deterministically from
// cfg.Rng (so a seed reproduces the same drivers). Defaults are applied for the
// injectable clock and RNG so production wiring stays terse.
func New(cfg Config) *Simulator {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Rng == nil {
		cfg.Rng = rand.New(rand.NewSource(1))
	}
	if cfg.Source == "" {
		cfg.Source = "timing"
	}
	drivers := make([]string, cfg.Drivers)
	for i := range drivers {
		drivers[i] = newFixtureMasterID(cfg.Rng)
	}
	return &Simulator{cfg: cfg, drivers: drivers}
}

// DriverIDs returns the fixture masterIds (for tests / logging).
func (s *Simulator) DriverIDs() []string { return s.drivers }

func (s *Simulator) now() time.Time { return s.cfg.Now() }

// Run drives continuous sessions until ctx is cancelled: generate a session, emit
// each event through the outbox (paced by Tick), pause SessionGap, repeat. It
// returns nil on graceful cancellation. A failed enqueue stops the run with the
// error (the caller decides; in practice the outbox enqueue only fails on a real
// DB fault).
func (s *Simulator) Run(ctx context.Context) error {
	s.logInfo("simulator started", "drivers", len(s.drivers), "sessionLaps", s.cfg.SessionLaps)
	for {
		if ctx.Err() != nil {
			s.logInfo("simulator stopped")
			return nil
		}
		session := s.GenerateSession(s.now())
		for _, env := range session {
			if err := s.cfg.Enqueue(ctx, env); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("simulator enqueue %s: %w", env.Type, err)
			}
			if !sleep(ctx, s.cfg.Tick) {
				return nil
			}
		}
		if !sleep(ctx, s.cfg.SessionGap) {
			return nil
		}
	}
}

// GenerateSession produces one complete session's events in time order:
// session.started, then one lap.recorded per counted lap across all drivers
// (interleaved by crossing time), then session.ended with a per-driver summary.
// Pure and deterministic given the base time and the RNG state — no I/O.
func (s *Simulator) GenerateSession(base time.Time) []messaging.Envelope {
	sessionID := "sim-" + base.UTC().Format("20060102T150405.000Z")
	correlationID := uuid.Must(uuid.NewV7()).String()

	// 1) Generate each driver's crossing times. The first crossing is the
	//    out-lap (start marker); SessionLaps more crossings follow, each a drawn
	//    lap time later. Crossing times are integer-ms offsets so the lap delta
	//    equals the drawn lap time exactly.
	type crossing struct {
		driver string
		at     time.Time
	}
	var crossings []crossing
	for _, drv := range s.drivers {
		offsetMs := 0
		// stagger out-laps slightly per driver for a stable, realistic order
		offsetMs += s.cfg.Rng.Intn(1000)
		crossings = append(crossings, crossing{driver: drv, at: base.Add(time.Duration(offsetMs) * time.Millisecond)})
		for lap := 0; lap < s.cfg.SessionLaps; lap++ {
			offsetMs += s.drawLapMs()
			crossings = append(crossings, crossing{driver: drv, at: base.Add(time.Duration(offsetMs) * time.Millisecond)})
		}
	}

	// 2) Emit in global time order (realistic: events arrive as drivers cross).
	sort.SliceStable(crossings, func(i, j int) bool { return crossings[i].at.Before(crossings[j].at) })

	trackers := make(map[string]*domain.LapTracker, len(s.drivers))
	for _, drv := range s.drivers {
		trackers[drv] = &domain.LapTracker{}
	}

	out := make([]messaging.Envelope, 0, len(crossings)+2)
	out = append(out, messaging.NewSessionStartedEnvelope(s.cfg.Source, correlationID, sessionID, base))

	type agg struct {
		best  int64
		count int
	}
	summaryByDriver := map[string]*agg{}
	var lastAt time.Time = base

	for _, c := range crossings {
		lap, ok := trackers[c.driver].Cross(c.at)
		if !ok {
			continue // out-lap: recorded, not a counted lap
		}
		out = append(out, messaging.NewLapRecordedEnvelope(
			s.cfg.Source, correlationID, c.driver, sessionID, lap.LapNumber, lap.LapTimeMs, nil, c.at))
		a := summaryByDriver[c.driver]
		if a == nil {
			a = &agg{best: lap.LapTimeMs}
			summaryByDriver[c.driver] = a
		}
		if lap.LapTimeMs < a.best {
			a.best = lap.LapTimeMs
		}
		a.count++
		lastAt = c.at
	}

	// 3) Session ends after the last crossing, carrying a minimal per-driver summary.
	summary := make([]messaging.SessionSummaryRow, 0, len(s.drivers))
	for _, drv := range s.drivers {
		if a := summaryByDriver[drv]; a != nil {
			summary = append(summary, messaging.SessionSummaryRow{MasterID: drv, BestLapMs: a.best, LapCount: a.count})
		}
	}
	out = append(out, messaging.NewSessionEndedEnvelope(s.cfg.Source, correlationID, sessionID, lastAt, summary))
	return out
}

// drawLapMs draws one lap time from the configured normal distribution, clamped
// to >= 1ms (the lap.recorded schema requires lapTimeMs >= 1). The clamp also
// guards against a low-mean/high-stddev draw going non-positive.
func (s *Simulator) drawLapMs() int {
	ms := int(s.cfg.Rng.NormFloat64()*float64(s.cfg.LapStddevMs) + float64(s.cfg.LapMeanMs))
	if ms < 1 {
		ms = 1
	}
	return ms
}

func (s *Simulator) logInfo(msg string, args ...any) {
	if s.cfg.Log != nil {
		s.cfg.Log.Info(msg, args...)
	}
}

// newFixtureMasterID builds a lowercase UUID v4 from the RNG. It is a FIXTURE id
// for the simulator only — never an Identity-issued canonical masterId.
func newFixtureMasterID(rng *rand.Rand) string {
	var b [16]byte
	for i := range b {
		b[i] = byte(rng.Intn(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10x
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// sleep waits d (interruptible by ctx). It returns false if ctx was cancelled
// during the wait, true otherwise. d <= 0 returns immediately (still honoring a
// cancelled ctx) so tests run with no real delay.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
