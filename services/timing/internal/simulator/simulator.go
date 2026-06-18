// Package simulator is Timing's development simulator (FR40): with no hardware,
// it generates a realistic stream of gate check-ins and start-finish crossings for
// N fixture drivers and emits the platform's domain events — session.started,
// driver.checked_in (one per driver at the gate), lap.recorded (one per counted lap),
// session.ended — through the Story-1.4 transactional outbox. It runs continuous
// sessions so the whole platform has ongoing end-to-end activity in dev and demos.
//
// Register-first (Epic 2, Story 2.3 / Q&A Round 32): the simulator no longer mints
// its own ids. Before a driver goes on track it RESOLVES that driver's canonical
// masterId via Identity (the injected Resolve seam — in production a bus request/reply:
// publish identity.lookup_requested, consume identity.resolved), and then uses the real
// Identity-issued masterId for driver.checked_in and every lap.recorded. QR drivers
// carry the masterId directly; transponder drivers' hardware-id->masterId mapping is
// seeded (the SeedTransponder seam) so the gate resolves it. This is the happy
// register-then-check-in chain only; register-FIRST enforcement + the unknown-token
// operator exception remain Story 2.5.
//
// Determinism: generation is driven by an injected *rand.Rand and a virtual clock, so
// a seed reproduces a session exactly. Emails (the natural key Identity de-dups on) are
// derived from the driver index, so a given driver always resolves to the same id.
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
	Drivers      int           // N fixture drivers (>= 1)
	Transponders int           // how many of the N drivers use a transponder (rest = QR); 0 = all QR
	LapMeanMs    int           // normal-distribution mean lap time
	LapStddevMs  int           // normal-distribution stddev
	SessionLaps  int           // counted laps per driver before the session ends
	MinLapTimeMs int           // lap-validity filter threshold (FR35); 0 = off
	Tick         time.Duration // wall-clock pacing between emitted events (0 = no pacing)
	SessionGap   time.Duration // pause between sessions in the continuous loop
	Source       string        // envelope `source` (== service name, "timing")
	Rng          *rand.Rand    // injected RNG (deterministic under a seed)
	Now          func() time.Time
	Enqueue      func(ctx context.Context, env messaging.Envelope) error // outbox seam (domain events)
	// Resolve performs the Identity register-first lookup for an email and returns the
	// canonical masterId (idempotent: same email -> same id). In production this is a
	// bus request/reply (identity.lookup_requested -> identity.resolved); in tests it is
	// a deterministic fake. Required.
	Resolve func(ctx context.Context, email string) (masterID string, err error)
	// SeedTransponder binds a transponder driver's hardware id to its resolved masterId
	// in Timing's local map (so the gate resolves it). The generic hand-out trigger is
	// Story 2.4; here the simulator hands out its own transponders at registration.
	SeedTransponder func(ctx context.Context, transponderID, masterID string) error
	Log             *slog.Logger
}

// driver is one simulated racer. masterID is empty until Prepare resolves it.
type driver struct {
	email         string
	method        string // messaging.CheckInMethodQR | CheckInMethodTransponder
	transponderID string // hardware id for a transponder driver; "" for QR
	masterID      string // the REAL Identity-issued id, filled by Prepare
}

// Simulator generates and emits simulated sessions. Construct it with New.
type Simulator struct {
	cfg     Config
	drivers []driver
}

// New builds a simulator with N driver descriptors derived deterministically from the
// index: a stable email (the Identity natural key) and a check-in method (the first
// cfg.Transponders drivers use a transponder, the rest QR). It mints NO ids — those are
// resolved from Identity in Prepare (register-first, Q&A Round 32).
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
	drivers := make([]driver, cfg.Drivers)
	for i := range drivers {
		d := driver{
			email:  fmt.Sprintf("sim-driver-%d@pitwall.test", i+1),
			method: messaging.CheckInMethodQR,
		}
		if i < cfg.Transponders {
			d.method = messaging.CheckInMethodTransponder
			d.transponderID = fmt.Sprintf("TP-SIM-%d", i+1)
		}
		drivers[i] = d
	}
	return &Simulator{cfg: cfg, drivers: drivers}
}

// DriverIDs returns the resolved masterIds (empty before Prepare; for tests / logging).
func (s *Simulator) DriverIDs() []string {
	ids := make([]string, len(s.drivers))
	for i, d := range s.drivers {
		ids[i] = d.masterID
	}
	return ids
}

func (s *Simulator) now() time.Time { return s.cfg.Now() }

// Prepare resolves every driver's canonical masterId via Identity (once — Identity is
// idempotent) and seeds each transponder driver's hardware-id->masterId mapping so the
// gate can resolve it. It is the register-first step; Run calls it before the first
// session. Resolve BLOCKS per driver until Identity replies or ctx is cancelled
// (register-first: a driver never goes on track without an id) — a transient stall is
// re-tried + escalated to an alert by the resolver, while a PERMANENT publish failure
// (a malformed lookup) returns immediately so Prepare fails fast and Run logs it.
func (s *Simulator) Prepare(ctx context.Context) error {
	for i := range s.drivers {
		id, err := s.cfg.Resolve(ctx, s.drivers[i].email)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", s.drivers[i].email, err)
		}
		s.drivers[i].masterID = id
		if s.drivers[i].method == messaging.CheckInMethodTransponder {
			if err := s.cfg.SeedTransponder(ctx, s.drivers[i].transponderID, id); err != nil {
				return fmt.Errorf("seed transponder %s: %w", s.drivers[i].transponderID, err)
			}
		}
	}
	s.logInfo("simulator drivers registered", "drivers", len(s.drivers), "transponders", s.cfg.Transponders)
	return nil
}

// Run prepares (register-first) then drives continuous sessions until ctx is cancelled:
// generate a session, emit each event through the outbox (paced by Tick), pause
// SessionGap, repeat. It returns nil on graceful cancellation.
func (s *Simulator) Run(ctx context.Context) error {
	if err := s.Prepare(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("simulator prepare: %w", err)
	}
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

// GenerateSession produces one complete session's events in time order: session.started,
// one driver.checked_in per driver at the gate, then one lap.recorded per counted lap
// across all drivers (interleaved by crossing time), then session.ended with a per-driver
// summary. Pure and deterministic given the base time, the RNG state, and the resolved
// driver masterIds (set by Prepare) — no I/O.
func (s *Simulator) GenerateSession(base time.Time) []messaging.Envelope {
	sessionID := "sim-" + base.UTC().Format("20060102T150405.000Z")
	correlationID := uuid.Must(uuid.NewV7()).String()

	out := make([]messaging.Envelope, 0, len(s.drivers)*(s.cfg.SessionLaps+1)+2)
	out = append(out, messaging.NewSessionStartedEnvelope(s.cfg.Source, correlationID, sessionID, base))

	// Gate check-in: every driver is identified at the entry gate when the session
	// starts (before any lap). QR carries the masterId directly; a transponder driver
	// carries its hardware id (resolved via the local map seeded in Prepare).
	for _, d := range s.drivers {
		var tp *string
		if d.method == messaging.CheckInMethodTransponder {
			id := d.transponderID
			tp = &id
		}
		out = append(out, messaging.NewCheckedInEnvelope(s.cfg.Source, correlationID, d.masterID, d.method, tp, base))
	}

	// Generate each driver's crossing times. The first crossing is the out-lap (start
	// marker); SessionLaps more crossings follow, each a drawn lap time later.
	type crossing struct {
		driver string
		at     time.Time
	}
	var crossings []crossing
	for _, d := range s.drivers {
		offsetMs := s.cfg.Rng.Intn(1000) // stagger out-laps slightly per driver
		crossings = append(crossings, crossing{driver: d.masterID, at: base.Add(time.Duration(offsetMs) * time.Millisecond)})
		for lap := 0; lap < s.cfg.SessionLaps; lap++ {
			offsetMs += s.drawLapMs()
			crossings = append(crossings, crossing{driver: d.masterID, at: base.Add(time.Duration(offsetMs) * time.Millisecond)})
		}
	}

	// Emit in global time order (realistic: events arrive as drivers cross).
	sort.SliceStable(crossings, func(i, j int) bool { return crossings[i].at.Before(crossings[j].at) })

	trackers := make(map[string]*domain.LapTracker, len(s.drivers))
	for _, d := range s.drivers {
		trackers[d.masterID] = &domain.LapTracker{MinLapTimeMs: int64(s.cfg.MinLapTimeMs)}
	}

	type agg struct {
		best  int64
		count int
	}
	summaryByDriver := map[string]*agg{}
	var lastAt time.Time = base

	for _, c := range crossings {
		lap, outcome := trackers[c.driver].Cross(c.at)
		if outcome != domain.Counted {
			// StartMarker (out-lap) or Rejected (sub-MIN bounce): no lap emitted.
			continue
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

	// Session ends after the last crossing, carrying a minimal per-driver summary.
	summary := make([]messaging.SessionSummaryRow, 0, len(s.drivers))
	for _, d := range s.drivers {
		if a := summaryByDriver[d.masterID]; a != nil {
			summary = append(summary, messaging.SessionSummaryRow{MasterID: d.masterID, BestLapMs: a.best, LapCount: a.count})
		}
	}
	out = append(out, messaging.NewSessionEndedEnvelope(s.cfg.Source, correlationID, sessionID, lastAt, summary))
	return out
}

// drawLapMs draws one lap time from the configured normal distribution, clamped to
// >= 1ms (the lap.recorded schema requires lapTimeMs >= 1).
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

// sleep waits d (interruptible by ctx). It returns false if ctx was cancelled during
// the wait, true otherwise. d <= 0 returns immediately (still honoring a cancelled ctx).
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
