//go:build integration

// Out-of-session lap reconciliation integration (Story 3.6, FR83/NFR24) against a REAL
// RabbitMQ broker. Proves the physical-reality-wins path end-to-end: with the out-of-session
// knob on, the simulator injects known-driver crossings during the inter-session gap while it
// thinks no session is active, and Timing reconciles by auto-starting a DISTINCT reconciled
// session ("sim-oos-…") — a second session.started/ended pair carrying real lap.recorded(s)
// through the Story-1.4 outbox. The out-of-session lap is accepted, never dropped. No new
// contract event and no durable table are involved (Q39.3/Q39.4) — the lap.recorded and
// session.started ARE the durable record.
package integration

import (
	"context"
	"encoding/json"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/relay"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/simulator"
)

func TestSimulatorOutOfSessionLapAcceptedAndReconciled(t *testing.T) {
	const drivers, laps, outOfSessionLaps = 3, 5, 2
	amqpURL := startBroker(t)
	store, db, closeDB := openStore(t, filepath.Join(t.TempDir(), "timing.db"))
	defer closeDB()

	deliveries, closeConsumer := bindConsumerAll(t, amqpURL)
	defer closeConsumer()

	pub, confirmCh := dialConfirm(t, amqpURL)
	defer pub.Close()
	defer confirmCh.Close()

	r := relay.New(relay.Config{
		Store:    store,
		Validate: validatorFor(t),
		Publish:  confirmCh.PublishConfirmed,
		Interval: 50 * time.Millisecond,
		Log:      logging.New(testWriter{t}, "timing", itCorrelationID, "error"),
	})
	enqueue := relay.NewEnqueuer(db, store, validatorFor(t), r)

	simLog := logging.New(testWriter{t}, "timing", itCorrelationID, "error")
	sim := simulator.New(simulator.Config{
		Drivers:          drivers,
		LapMeanMs:        45000,
		LapStddevMs:      2000,
		SessionLaps:      laps,
		MinLapTimeMs:     10000,
		Tick:             0,
		SessionGap:       time.Hour, // block after the reconciled session so only one of each is emitted
		Source:           "timing",
		Rng:              rand.New(rand.NewSource(53)),
		Now:              time.Now,
		Enqueue:          enqueue,
		Resolve:          fakeResolveID,
		OutOfSessionLaps: outOfSessionLaps,
		Log:              simLog,
	})

	relayCtx, stopRelay := context.WithCancel(context.Background())
	defer stopRelay()
	simCtx, stopSim := context.WithCancel(context.Background())
	defer stopSim()
	go func() { _ = r.Run(relayCtx) }()
	go func() { _ = sim.Run(simCtx) }()

	validate := validatorFor(t)
	oosStarts, oosEnds, oosLaps := 0, 0, 0
	oosSessionID := ""
	deadline := time.After(30 * time.Second)
	for {
		select {
		case d := <-deliveries:
			env, err := messaging.DecodeIncoming(d.Body)
			if err != nil {
				t.Fatalf("delivery not a valid envelope: %v", err)
			}
			if err := validate(d.Body); err != nil {
				t.Fatalf("event %q off the bus failed /contract validation: %v", env.Type, err)
			}
			switch env.Type {
			case messaging.SessionStartedRoutingKey:
				var sd messaging.SessionStartedData
				if err := json.Unmarshal(env.Data, &sd); err != nil {
					t.Fatalf("decode session.started data: %v", err)
				}
				if strings.HasPrefix(sd.SessionID, "sim-oos-") {
					oosStarts++
					oosSessionID = sd.SessionID
				}
			case messaging.LapRecordedRoutingKey:
				var ld messaging.LapRecordedData
				if err := json.Unmarshal(env.Data, &ld); err != nil {
					t.Fatalf("decode lap.recorded data: %v", err)
				}
				if oosSessionID != "" && ld.SessionID == oosSessionID {
					oosLaps++
				}
			case messaging.SessionEndedRoutingKey:
				var ed messaging.SessionEndedData
				if err := json.Unmarshal(env.Data, &ed); err != nil {
					t.Fatalf("decode session.ended data: %v", err)
				}
				if strings.HasPrefix(ed.SessionID, "sim-oos-") {
					oosEnds++
					stopSim()
					if oosStarts != 1 || oosEnds != 1 {
						t.Fatalf("reconciled session.started/ended off the bus = %d/%d, want 1/1", oosStarts, oosEnds)
					}
					if oosLaps < 1 {
						t.Fatalf("reconciled lap.recorded count = %d, want >= 1 (accepted, never dropped)", oosLaps)
					}
					assertAllSentEventually(t, store)
					stopRelay()
					return
				}
			}
		case <-deadline:
			t.Fatalf("did not observe a reconciled out-of-session session in time; saw %d oos starts, %d laps", oosStarts, oosLaps)
		}
	}
}
