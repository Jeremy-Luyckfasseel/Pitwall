//go:build integration

// The e2e conformance suite (Story 1.11, AR16/NFR23). Each scenario in
// scenarios/*.yaml is run here against ONE shared real RabbitMQ (testcontainers)
// driving the REAL built service binaries. Tests WAIT ON OBSERVABLE CONDITIONS
// (the served board, process state) — never time.Sleep as an assertion (a sleep
// is only ever a poll interval). Run: `make smoke` or
// `go test -tags=integration ./...` (Docker required).
package conformance

import (
	"testing"
	"time"
)

// runSmoke is the happy-path e2e smoke and the core required gate (AC2/AC3):
// the REAL Timing simulator -> bus -> REAL Leaderboard -> served board. It proves
// the walking skeleton end-to-end through the real entrypoints.
func runSmoke(t *testing.T, sc Scenario) {
	sim := mustSimulator(t, sc)
	br := startBroker(t)

	// Real Leaderboard binary (consumer + SSE board).
	lb := startLeaderboard(t, br.amqpURL)

	// Real Timing binary in simulator mode (seed-deterministic lap producer).
	startTimingSimulator(t, br.amqpURL, sim)

	// Observable 1: the board converges to ALL N drivers (laps flowed across the bus).
	waitUntil(t, "board shows all simulated drivers", 60*time.Second, func() bool {
		s, err := lb.trySnapshot()
		return err == nil && len(s.Rows) == sc.Expect.BoardDrivers
	})

	// Observable 2: ranking is best-lap ascending — asserted on a snapshot CONFIRMED
	// to hold all N drivers (not a possibly-partial between-waits board).
	full := lb.snapshot(t)
	if len(full.Rows) != sc.Expect.BoardDrivers {
		t.Fatalf("expected %d drivers on the board, got %d", sc.Expect.BoardDrivers, len(full.Rows))
	}
	if sc.Expect.RankedAscending {
		assertRankedAscending(t, full)
	}

	// Observable 3: the session ends -> status flips to "finished" (FR45).
	if sc.Expect.SessionFinished {
		waitUntil(t, "session status flips to finished", 60*time.Second, func() bool {
			s, err := lb.trySnapshot()
			return err == nil && s.Session != nil && s.Session.Status == "finished"
		})
	}
}
