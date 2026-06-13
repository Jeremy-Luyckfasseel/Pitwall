//go:build integration

package conformance

import (
	"testing"
	"time"
)

// The four AR16 reliability scenarios, each driving the REAL Leaderboard binary
// against one shared real RabbitMQ. They assert OBSERVABLE outcomes on the served
// board (waiting on conditions via trySnapshot, never sleeping). Dedupe is made
// falsifiable on the board by republishing a duplicate under the SAME envelope id
// with a FASTER time: if the inbox dedupes on envelope id, the board keeps the
// ORIGINAL time.

// runPeerDown: the bus goes down WHILE laps are still being produced; Timing
// buffers them (no loss) and the board freezes flagged stale; on restore the board
// reconverges to every driver (M3/M9). The scenario paces the simulator slowly
// (SimulatorSpec.TickMs) so the kill lands mid-session — otherwise a fast session
// would fully drain before the kill and nothing would be buffered (false-green).
func runPeerDown(t *testing.T, sc Scenario) {
	sim := mustSimulator(t, sc)
	br := startBroker(t)
	lb := startLeaderboard(t, br.amqpURL)
	startTimingSimulator(t, br.amqpURL, sim)

	// Let at least one lap reach the board (a live session is flowing) but the
	// session is NOT yet complete (slow tick guarantees more laps are still coming).
	waitUntil(t, "board has at least one driver before the kill", 60*time.Second, func() bool {
		s, err := lb.trySnapshot()
		return err == nil && len(s.Rows) >= 1
	})

	// KILL the bus mid-session: the served bundle must flip stale and FREEZE on
	// last-known (rows preserved). Laps produced now buffer in Timing's outbox.
	preRows := len(lb.snapshot(t).Rows)
	br.stop(t)
	waitUntil(t, "served board flips stale on bus-down", 30*time.Second, func() bool {
		s, err := lb.trySnapshot()
		return err == nil && s.Stale
	})
	if got := len(lb.snapshot(t).Rows); got < preRows {
		t.Fatalf("stale board lost last-known rows: had %d, now %d (must freeze, not wipe)", preRows, got)
	}

	// RESTORE the bus: Timing flushes its buffered laps, the board reconverges to
	// all N drivers, and the stale flag clears (M9 reconverge after broker-ready).
	br.start(t)
	waitUntil(t, "board reconverges to all drivers, stale cleared", 90*time.Second, func() bool {
		s, err := lb.trySnapshot()
		return err == nil && !s.Stale && len(s.Rows) >= sc.Expect.BoardDrivers
	})
	if sc.Expect.RankedAscending {
		assertRankedAscending(t, lb.snapshot(t))
	}
}

// runInboxDup: a duplicate lap.recorded (same envelope id, faster time) is a
// no-op — the board keeps the original time (M6).
func runInboxDup(t *testing.T, sc Scenario) {
	fx := mustFixture(t, sc)
	di, dupMs := mustDuplicate(t, fx)
	br := startBroker(t)
	lb := startLeaderboard(t, br.amqpURL)
	pub := dialPublisher(t, br.amqpURL)

	pub.sessionStarted(t, fx.Session)
	ids := publishLaps(t, pub, fx) // one unique (confirmed) envelope id per lap

	waitUntil(t, "board shows all unique drivers", 30*time.Second, func() bool {
		s, err := lb.trySnapshot()
		return err == nil && len(s.Rows) >= len(fx.Laps)
	})

	// Republish laps[di] under the SAME envelope id but a FASTER time. Dedupe must
	// hold: the duplicate driver keeps its ORIGINAL time, no extra driver appears.
	dup := fx.Laps[di]
	pub.lap(t, ids[di], dup.Master, fx.Session, 99, dupMs)

	assertBoardStable(t, lb, len(fx.Laps), sc, func(s snapshotView) bool {
		return masterIDs(s)[dup.Master] == dup.LapMs
	}, "duplicate ignored, original time kept")
}

// runPublishRedeliver: persistent laps published BEFORE a state-preserving broker
// bounce survive the restart and are delivered after it (no loss across a broker
// restart, M4); a same-id faster-time republish after the bounce is still deduped
// (M6). The bounce happens BEFORE convergence (the harness publishes with broker
// confirms, so the messages are durably persisted) — so a service that lost
// persistent messages on restart, or double-applied across it, would fail here.
func runPublishRedeliver(t *testing.T, sc Scenario) {
	fx := mustFixture(t, sc)
	di, dupMs := mustDuplicate(t, fx)
	br := startBroker(t)
	lb := startLeaderboard(t, br.amqpURL)
	pub := dialPublisher(t, br.amqpURL)

	// Publish (confirmed → durably persisted) then bounce BEFORE the board has
	// necessarily consumed them: the persistent messages must survive in the
	// durable queue and be delivered after restart.
	pub.sessionStarted(t, fx.Session)
	ids := publishLaps(t, pub, fx)
	br.stop(t)
	br.start(t)

	// All laps must converge after the restart — no loss across the bounce (M4).
	waitUntil(t, "board converges to all drivers after the broker bounce", 90*time.Second, func() bool {
		s, err := lb.trySnapshot()
		return err == nil && !s.Stale && len(s.Rows) >= len(fx.Laps)
	})

	// A same-id republish after the bounce is still deduped (M6): original time kept.
	pub2 := dialPublisher(t, br.amqpURL) // fresh connection to the restored broker
	dup := fx.Laps[di]
	pub2.lap(t, ids[di], dup.Master, fx.Session, 99, dupMs)
	assertBoardStable(t, lb, len(fx.Laps), sc, func(s snapshotView) bool {
		return !s.Stale && masterIDs(s)[dup.Master] == dup.LapMs
	}, "redelivered/persistent lap applied exactly once, original time kept")
}

// runCrashAfterAck: the consumer process is killed after applying some laps and
// restarted on the same DB; it rebuilds from its durable read-model and replays
// past the durable inbox marker — no loss, no double-count, even for a redelivered
// already-applied lap (M4 + M6).
func runCrashAfterAck(t *testing.T, sc Scenario) {
	fx := mustFixture(t, sc)
	di, dupMs := mustDuplicate(t, fx)
	acked := mustAckedBeforeCrash(t, fx)
	br := startBroker(t)
	lb := startLeaderboard(t, br.amqpURL)
	pub := dialPublisher(t, br.amqpURL)

	pub.sessionStarted(t, fx.Session)
	ids := make([]string, len(fx.Laps))
	for i := range fx.Laps {
		ids[i] = newUUID(t)
	}
	// Publish + apply the first `acked` laps, then crash.
	for i := 0; i < acked; i++ {
		pub.lap(t, ids[i], fx.Laps[i].Master, fx.Session, i+1, fx.Laps[i].LapMs)
	}
	waitUntil(t, "first laps applied before crash", 30*time.Second, func() bool {
		s, err := lb.trySnapshot()
		return err == nil && len(s.Rows) >= acked
	})

	// Hard crash + restart on the SAME DB.
	lb.restart(t)

	// After restart: publish the rest, then republish laps[di]'s id with a faster
	// time. Durable read-model + inbox marker must survive: every driver appears
	// (no loss) and laps[di] keeps its original time (dedupe survived the crash).
	for i := acked; i < len(fx.Laps); i++ {
		pub.lap(t, ids[i], fx.Laps[i].Master, fx.Session, i+1, fx.Laps[i].LapMs)
	}
	dup := fx.Laps[di]
	pub.lap(t, ids[di], dup.Master, fx.Session, 99, dupMs)

	assertBoardStable(t, lb, len(fx.Laps), sc, func(s snapshotView) bool {
		return masterIDs(s)[dup.Master] == dup.LapMs
	}, "post-crash replay: no loss, original time kept")
}

// --- guards (clear failures instead of nil-pointer/out-of-range panics) -------

func mustSimulator(t *testing.T, sc Scenario) SimulatorSpec {
	t.Helper()
	if sc.Simulator == nil {
		t.Fatalf("scenario %q requires a `simulator:` block", sc.Name)
	}
	return *sc.Simulator
}

func mustFixture(t *testing.T, sc Scenario) *FixtureSpec {
	t.Helper()
	if sc.Fixture == nil || len(sc.Fixture.Laps) == 0 {
		t.Fatalf("scenario %q requires a non-empty `fixture.laps`", sc.Name)
	}
	return sc.Fixture
}

// mustDuplicate returns the duplicate lap index + its (faster) republish time,
// failing clearly if the fixture omits them or the index is out of range.
func mustDuplicate(t *testing.T, fx *FixtureSpec) (int, int64) {
	t.Helper()
	if fx.DuplicateIndex == nil || fx.DuplicateLapMs == nil {
		t.Fatal("fixture requires `duplicateIndex` + `duplicateLapMs` for the dedupe probe")
	}
	di := *fx.DuplicateIndex
	if di < 0 || di >= len(fx.Laps) {
		t.Fatalf("duplicateIndex %d out of range (laps=%d)", di, len(fx.Laps))
	}
	return di, *fx.DuplicateLapMs
}

func mustAckedBeforeCrash(t *testing.T, fx *FixtureSpec) int {
	t.Helper()
	if fx.AckedBeforeCrash == nil {
		t.Fatal("fixture requires `ackedBeforeCrash`")
	}
	n := *fx.AckedBeforeCrash
	if n < 0 || n > len(fx.Laps) {
		t.Fatalf("ackedBeforeCrash %d out of range (laps=%d)", n, len(fx.Laps))
	}
	return n
}

// --- shared scenario helpers --------------------------------------------------

// publishLaps publishes each fixture lap with a fresh (confirmed) envelope id,
// returning the ids so a later duplicate can reuse one.
func publishLaps(t *testing.T, pub *harnessPublisher, fx *FixtureSpec) []string {
	t.Helper()
	ids := make([]string, len(fx.Laps))
	for i, l := range fx.Laps {
		ids[i] = newUUID(t)
		pub.lap(t, ids[i], l.Master, fx.Session, i+1, l.LapMs)
	}
	return ids
}

// assertBoardStable first waits until the board has wantDrivers AND the invariant
// holds, then re-checks it stays true across a short hold — so a late
// (incorrectly-applied) duplicate would be caught rather than missed by a single
// snapshot. Transient SSE read errors during the hold are tolerated (re-polled),
// never fatal; only a genuine invariant break fails. Ranking is also asserted when
// the scenario expects it.
func assertBoardStable(t *testing.T, lb *svcProc, wantDrivers int, sc Scenario, invariant func(snapshotView) bool, desc string) {
	t.Helper()
	waitUntil(t, "board reaches: "+desc, 60*time.Second, func() bool {
		s, err := lb.trySnapshot()
		return err == nil && len(s.Rows) == wantDrivers && invariant(s)
	})
	if sc.Expect.RankedAscending {
		assertRankedAscending(t, lb.snapshot(t))
	}
	// Hold: the invariant must remain true (a mis-applied duplicate would break it).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, err := lb.trySnapshot()
		if err != nil {
			time.Sleep(150 * time.Millisecond)
			continue
		}
		if len(s.Rows) != wantDrivers || !invariant(s) {
			t.Fatalf("board not stable (%s): rows=%d want=%d", desc, len(s.Rows), wantDrivers)
		}
		time.Sleep(150 * time.Millisecond)
	}
}
