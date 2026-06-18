//go:build integration

// Simulator integration (Story 1.5) against a REAL RabbitMQ broker and a real
// on-disk SQLite database (testcontainers — no sleeps). Proves the producer half
// of Epic 1's vertical slice end-to-end: the simulator generates a coherent,
// ordered, contract-valid session.started -> lap.recorded* -> session.ended
// stream onto timing.events through the Story-1.4 outbox, and every row ends sent.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/relay"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/simulator"
)

// fakeResolveID stands in for the Identity request/reply: a deterministic, stable
// lowercase UUID v4 per email (this integration test exercises the simulator's outbox
// stream, not the real Identity round-trip — that is the conformance checkin-chain
// scenario). The real bus resolver is wired in cmd/timing.
func fakeResolveID(_ context.Context, email string) (string, error) {
	h := fnv.New128a()
	_, _ = h.Write([]byte(email))
	var b [16]byte
	copy(b[:], h.Sum(nil))
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// AC1/AC3/AC4: with the simulator on, a real broker receives one full session in
// order — session.started, then drivers*laps lap.recorded, then session.ended —
// all contract-valid, and the outbox drains to all-sent.
func TestSimulatorStreamsValidSessionThroughOutbox(t *testing.T) {
	const drivers, laps = 2, 3
	amqpURL := startBroker(t)
	store, db, closeDB := openStore(t, filepath.Join(t.TempDir(), "timing.db"))
	defer closeDB()

	deliveries, closeConsumer := bindConsumerAll(t, amqpURL)
	defer closeConsumer()

	pub, confirmCh := dialConfirm(t, amqpURL)
	defer pub.Close()
	defer confirmCh.Close()

	// Real relay (publishes to timing.events) + the real producer seam.
	r := relay.New(relay.Config{
		Store:    store,
		Validate: validatorFor(t),
		Publish:  confirmCh.PublishConfirmed,
		Interval: 50 * time.Millisecond,
		Log:      logging.New(testWriter{t}, "timing", itCorrelationID, "error"),
	})
	enqueue := relay.NewEnqueuer(db, store, validatorFor(t), r)

	sim := simulator.New(simulator.Config{
		Drivers:      drivers,
		LapMeanMs:    45000,
		LapStddevMs:  2000,
		SessionLaps:  laps,
		MinLapTimeMs: 10000,     // filter active (Story 1.6) but below the mean -> rejects nothing (AC4)
		Tick:         0,         // stream as fast as the broker confirms (no sleeps)
		SessionGap:   time.Hour, // only one session within the test window
		Source:       "timing",
		Rng:          rand.New(rand.NewSource(1)),
		Now:          time.Now,
		Enqueue:      enqueue,
		Resolve:      fakeResolveID, // register-first (Story 2.3): real ids replace fixtures
		Log:          logging.New(testWriter{t}, "timing", itCorrelationID, "error"),
	})

	// Independent contexts: stopping the simulator (no new sessions) must NOT
	// cancel the relay mid-flight — the relay uses its ctx for the mark-sent DB
	// write, so a shared cancel could abort it and leave a row pending.
	relayCtx, stopRelay := context.WithCancel(context.Background())
	defer stopRelay()
	simCtx, stopSim := context.WithCancel(context.Background())
	defer stopSim()
	go func() { _ = r.Run(relayCtx) }()
	go func() { _ = sim.Run(simCtx) }()

	validate := validatorFor(t)
	wantLaps := drivers * laps
	var types []string
	gotLaps := 0
	deadline := time.After(30 * time.Second)
	for {
		select {
		case d := <-deliveries:
			var env messaging.Envelope
			if err := json.Unmarshal(d.Body, &env); err != nil {
				t.Fatalf("delivery not a valid envelope: %v", err)
			}
			if err := validate(d.Body); err != nil {
				t.Fatalf("event %q off the bus failed /contract validation: %v", env.Type, err)
			}
			types = append(types, env.Type)
			if env.Type == messaging.LapRecordedRoutingKey {
				gotLaps++
			}
			if env.Type == messaging.SessionEndedRoutingKey {
				// One full session captured — stop the simulator (no new
				// sessions), keep the relay running so it can finish marking
				// rows sent, then assert.
				stopSim()
				assertSessionShape(t, types, drivers, wantLaps)
				if gotLaps != wantLaps {
					t.Fatalf("lap.recorded count = %d, want %d", gotLaps, wantLaps)
				}
				assertAllSentEventually(t, store)
				stopRelay()
				return
			}
		case <-deadline:
			t.Fatalf("did not observe a full session in time; saw %d events: %v", len(types), types)
		}
	}
}

// assertSessionShape checks the register-first session shape (Story 2.3): the first
// event is session.started, the last is session.ended, exactly wantCheckIns
// driver.checked_in sit at the gate (all BEFORE any lap), and exactly wantLaps
// lap.recorded follow.
func assertSessionShape(t *testing.T, types []string, wantCheckIns, wantLaps int) {
	t.Helper()
	if len(types) < 2 {
		t.Fatalf("too few events: %v", types)
	}
	if types[0] != messaging.SessionStartedRoutingKey {
		t.Errorf("first event = %q, want session.started", types[0])
	}
	if types[len(types)-1] != messaging.SessionEndedRoutingKey {
		t.Errorf("last event = %q, want session.ended", types[len(types)-1])
	}
	checkIns, laps := 0, 0
	sawLap := false
	for _, ty := range types[1 : len(types)-1] {
		switch ty {
		case messaging.DriverCheckedInRoutingKey:
			checkIns++
			if sawLap {
				t.Errorf("driver.checked_in appeared AFTER a lap.recorded — check-ins must precede laps")
			}
		case messaging.LapRecordedRoutingKey:
			sawLap = true
			laps++
		default:
			t.Errorf("unexpected mid-stream event %q (want driver.checked_in then lap.recorded)", ty)
		}
	}
	if checkIns != wantCheckIns {
		t.Errorf("mid-stream driver.checked_in = %d, want %d", checkIns, wantCheckIns)
	}
	if laps != wantLaps {
		t.Errorf("mid-stream lap.recorded = %d, want %d", laps, wantLaps)
	}
}

// assertAllSentEventually polls the outbox until no rows remain pending (the
// relay marks a row sent right after its broker confirm, just before the
// consumer delivery is observed). Polls a readiness condition, never a sleep.
func assertAllSentEventually(t *testing.T, store *persistence.OutboxStore) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		pending, err := store.FetchPending(context.Background(), 100)
		if err != nil {
			t.Fatalf("FetchPending: %v", err)
		}
		if len(pending) == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%d rows still pending after the session; want all sent", len(pending))
		case <-tick.C:
		}
	}
}

// bindConsumerAll binds a temporary queue to every routing key on timing.events.
func bindConsumerAll(t *testing.T, amqpURL string) (<-chan amqp.Delivery, func()) {
	t.Helper()
	return bindConsumerKey(t, amqpURL, "#")
}
