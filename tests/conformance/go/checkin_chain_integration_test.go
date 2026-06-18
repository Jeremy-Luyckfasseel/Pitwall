//go:build integration

package conformance

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// busObserver subscribes to the REAL exchanges and records the canonical-id chain:
// the masterIds Identity hands out (identity.resolved on identity.events) and the
// gate check-ins Timing emits (driver.checked_in on timing.events, with their
// checkInMethod). It is the harness's observable for the Story 2.3 chain — distinct
// from the Leaderboard board (which only proves the laps landed).
type busObserver struct {
	conn *amqp.Connection
	ch   *amqp.Channel

	mu         sync.Mutex
	resolved   map[string]bool   // set of masterIds returned by identity.resolved
	checkedIn  map[string]string // masterId -> checkInMethod from driver.checked_in
	laps       map[string]bool   // set of masterIds seen on lap.recorded
	decodeErrs int               // envelopes that failed to decode (a wire-shape regression)
}

type observedEnvelope struct {
	Type string `json:"type"`
	Data struct {
		MasterID      string `json:"masterId"`
		CheckInMethod string `json:"checkInMethod"`
	} `json:"data"`
}

// startBusObserver binds auto-delete queues to timing.events (driver.checked_in) and
// identity.events (identity.resolved) and consumes them. It must be started BEFORE the
// services emit, so it captures the whole chain.
func startBusObserver(t *testing.T, amqpURL string) *busObserver {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("observer dial: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("observer channel: %v", err)
	}

	o := &busObserver{conn: conn, ch: ch, resolved: map[string]bool{}, checkedIn: map[string]string{}, laps: map[string]bool{}}
	o.bindAndConsume(t, "timing.events", "driver.checked_in")
	o.bindAndConsume(t, "timing.events", "lap.recorded")
	o.bindAndConsume(t, "identity.events", "identity.resolved")
	t.Cleanup(func() { _ = ch.Close(); _ = conn.Close() })
	return o
}

func (o *busObserver) bindAndConsume(t *testing.T, exchange, routingKey string) {
	t.Helper()
	// Declare durable topic to match how the services declare their own exchanges
	// (a mismatched declaration would 406); the observer may start before them.
	if err := o.ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("observer declare %s: %v", exchange, err)
	}
	q, err := o.ch.QueueDeclare("", false, true, true, false, nil) // server-named, exclusive, auto-delete
	if err != nil {
		t.Fatalf("observer queue for %s: %v", exchange, err)
	}
	if err := o.ch.QueueBind(q.Name, routingKey, exchange, false, nil); err != nil {
		t.Fatalf("observer bind %s/%s: %v", exchange, routingKey, err)
	}
	deliveries, err := o.ch.Consume(q.Name, "", true, true, false, false, nil) // autoAck (observe-only)
	if err != nil {
		t.Fatalf("observer consume %s: %v", q.Name, err)
	}
	go func() {
		for d := range deliveries {
			var env observedEnvelope
			if json.Unmarshal(d.Body, &env) != nil {
				o.mu.Lock()
				o.decodeErrs++ // a wire-shape regression — surfaced, not silently skipped
				o.mu.Unlock()
				continue
			}
			o.mu.Lock()
			switch env.Type {
			case "identity.resolved":
				o.resolved[env.Data.MasterID] = true
			case "driver.checked_in":
				o.checkedIn[env.Data.MasterID] = env.Data.CheckInMethod
			case "lap.recorded":
				o.laps[env.Data.MasterID] = true
			}
			o.mu.Unlock()
		}
	}()
}

func (o *busObserver) checkInCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.checkedIn)
}

func (o *busObserver) resolvedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.resolved)
}

// snapshot returns copies of the collected sets + the decode-error count (safe after a wait).
func (o *busObserver) snapshot() (resolved map[string]bool, checkedIn map[string]string, laps map[string]bool, decodeErrs int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	resolved = make(map[string]bool, len(o.resolved))
	for k := range o.resolved {
		resolved[k] = true
	}
	checkedIn = make(map[string]string, len(o.checkedIn))
	for k, v := range o.checkedIn {
		checkedIn[k] = v
	}
	laps = make(map[string]bool, len(o.laps))
	for k := range o.laps {
		laps[k] = true
	}
	return resolved, checkedIn, laps, o.decodeErrs
}

// runCheckinChain proves the canonical-id chain end-to-end (Story 2.3, AC1/AC2/AC3):
// Identity resolves each driver's masterId, the simulator emits driver.checked_in with
// that REAL id (QR-direct or transponder-resolved), laps flow, and the SAME id set
// reaches the Leaderboard board. The bus observer asserts identity.resolved →
// driver.checked_in → board are the same masterIds, with the expected qr/transponder split.
func runCheckinChain(t *testing.T, sc Scenario) {
	sim := mustSimulator(t, sc)
	br := startBroker(t)

	// Observe the chain BEFORE the services emit.
	obs := startBusObserver(t, br.amqpURL)

	lb := startLeaderboard(t, br.amqpURL)
	startTimingSimulator(t, br.amqpURL, sim) // also brings up the real Identity binary

	// The board converges to all N drivers (laps flowed across the bus).
	waitUntil(t, "board shows all simulated drivers", 90*time.Second, func() bool {
		s, err := lb.trySnapshot()
		return err == nil && len(s.Rows) == sc.Expect.BoardDrivers
	})
	// And the gate emitted one check-in per driver, and Identity resolved exactly N ids.
	waitUntil(t, "all drivers checked in at the gate", 90*time.Second, func() bool {
		return obs.checkInCount() == sc.Expect.CheckedIn
	})
	waitUntil(t, "Identity resolved exactly N ids", 90*time.Second, func() bool {
		return obs.resolvedCount() == sc.Expect.CheckedIn
	})

	full := lb.snapshot(t)
	if len(full.Rows) != sc.Expect.BoardDrivers {
		t.Fatalf("expected %d drivers on the board, got %d", sc.Expect.BoardDrivers, len(full.Rows))
	}
	if sc.Expect.RankedAscending {
		assertRankedAscending(t, full)
	}

	resolved, checkedIn, laps, decodeErrs := obs.snapshot()
	board := masterIDs(full)

	// 0) No undecodable events on the observed exchanges (a wire-shape regression would
	//    otherwise be masked as a generic timeout).
	if decodeErrs != 0 {
		t.Errorf("observer saw %d undecodable envelopes on timing.events/identity.events", decodeErrs)
	}

	// 1) Exactly the expected number of check-ins, with the expected method split.
	if len(checkedIn) != sc.Expect.CheckedIn {
		t.Fatalf("driver.checked_in count = %d, want %d", len(checkedIn), sc.Expect.CheckedIn)
	}
	transponders := 0
	for master, method := range checkedIn {
		switch method {
		case "transponder":
			transponders++
		case "qr":
		default:
			t.Errorf("driver %s checked in with unexpected method %q", master, method)
		}
	}
	if transponders != sc.Expect.TransponderCheckIns {
		t.Errorf("transponder check-ins = %d, want %d", transponders, sc.Expect.TransponderCheckIns)
	}

	// 2) The chain is the SAME id SET (equality, not containment): Identity resolved
	//    exactly the checked-in ids, every checked-in id is on the board, and the board
	//    holds exactly those ids — Identity is genuinely chained into Timing's output.
	if len(resolved) != sc.Expect.CheckedIn {
		t.Errorf("identity.resolved distinct ids = %d, want exactly %d", len(resolved), sc.Expect.CheckedIn)
	}
	for master := range checkedIn {
		if !resolved[master] {
			t.Errorf("checked-in masterId %s was never resolved by Identity (broken chain)", master)
		}
		if _, ok := board[master]; !ok {
			t.Errorf("checked-in masterId %s never reached the board", master)
		}
	}
	if len(board) != sc.Expect.CheckedIn {
		t.Errorf("board has %d drivers, want the %d checked-in ids", len(board), sc.Expect.CheckedIn)
	}

	// 3) AC3's lap clause: every lap.recorded carries a REAL Identity-resolved id (the
	//    simulator uses the resolved masterId for laps too, not a fixture).
	if len(laps) == 0 {
		t.Error("observed no lap.recorded events")
	}
	for master := range laps {
		if !resolved[master] {
			t.Errorf("lap.recorded masterId %s was never resolved by Identity (broken chain)", master)
		}
	}

	// 4) The session finishes (FR45).
	if sc.Expect.SessionFinished {
		waitUntil(t, "session status flips to finished", 90*time.Second, func() bool {
			s, err := lb.trySnapshot()
			return err == nil && s.Session != nil && s.Session.Status == "finished"
		})
	}
}
