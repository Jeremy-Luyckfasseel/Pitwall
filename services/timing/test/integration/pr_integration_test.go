//go:build integration

// Personal-record integration (Story 3.4) against a REAL RabbitMQ broker + real SQLite.
// Proves both halves of the PR loop end-to-end: (A) the simulator's live detection emits
// contract-valid personal_record.broken onto timing.events through the outbox and advances
// the local driver_prs copy; (B) a driver.pr_updated consumed off driver.events refreshes
// that local copy via the production PRRefreshHandler.
package integration

import (
	"context"
	"encoding/json"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/dlq"
	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/relay"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/simulator"
)

// AC1 end-to-end: with the PR subsystem wired, a real broker receives contract-valid
// personal_record.broken events (at least one per driver -- every driver's first counted
// lap is a first PR), and Timing's local driver_prs copy holds a PR for each driver.
func TestSimulatorEmitsPersonalRecordBrokenThroughOutbox(t *testing.T) {
	const drivers, laps = 3, 4
	amqpURL := startBroker(t)
	store, db, closeDB := openStore(t, filepath.Join(t.TempDir(), "timing.db"))
	defer closeDB()
	prStore := persistence.NewDriverPRStore(db)

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

	sim := simulator.New(simulator.Config{
		Drivers:      drivers,
		LapMeanMs:    45000,
		LapStddevMs:  4000,
		SessionLaps:  laps,
		MinLapTimeMs: 10000,
		Tick:         0,
		SessionGap:   time.Hour,
		Source:       "timing",
		Rng:          rand.New(rand.NewSource(1)),
		Now:          time.Now,
		Enqueue:      enqueue,
		Resolve:      fakeResolveID,
		ObservePR: func(ctx context.Context, masterID, sessionID string, lapTimeMs int64, at string) (bool, *int64, error) {
			return prStore.ObserveLap(ctx, masterID, sessionID, lapTimeMs, at)
		},
		Log: logging.New(testWriter{t}, "timing", itCorrelationID, "error"),
	})

	relayCtx, stopRelay := context.WithCancel(context.Background())
	defer stopRelay()
	simCtx, stopSim := context.WithCancel(context.Background())
	defer stopSim()
	go func() { _ = r.Run(relayCtx) }()
	go func() { _ = sim.Run(simCtx) }()

	validate := validatorFor(t)
	brokenBy := map[string]bool{}
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
			if env.Type == messaging.PersonalRecordBrokenRoutingKey {
				data := env.Data.(map[string]any)
				brokenBy[data["masterId"].(string)] = true
			}
			if env.Type == messaging.SessionEndedRoutingKey {
				stopSim()
				if len(brokenBy) != drivers {
					t.Fatalf("personal_record.broken seen for %d drivers, want %d", len(brokenBy), drivers)
				}
				// Every driver's local PR copy is populated.
				for masterID := range brokenBy {
					if _, ok, err := prStore.Get(context.Background(), masterID); err != nil || !ok {
						t.Fatalf("driver_prs missing local copy for %s (ok=%v err=%v)", masterID, ok, err)
					}
				}
				assertAllSentEventually(t, store)
				stopRelay()
				return
			}
		case <-deadline:
			t.Fatalf("did not observe a full session in time; broken drivers so far: %d", len(brokenBy))
		}
	}
}

// AC2 end-to-end: a driver.pr_updated consumed off driver.events refreshes Timing's local
// PR copy via the production PRRefreshHandler (latest-confirmed-wins).
func TestDriverPRUpdatedRefreshesLocalCopy(t *testing.T) {
	amqpURL := startBroker(t)
	_, db, closeDB := openStore(t, filepath.Join(t.TempDir(), "timing.db"))
	defer closeDB()
	prStore := persistence.NewDriverPRStore(db)

	const masterID = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
	ctx := context.Background()
	// Seed a slower local copy first (as live detection would).
	if _, _, err := prStore.ObserveLap(ctx, masterID, "sess-it", 45000, "2026-06-05T14:00:10.000Z"); err != nil {
		t.Fatalf("seed ObserveLap: %v", err)
	}

	log := logging.New(testWriter{t}, "timing", itCorrelationID, "error")
	bus, err := messaging.DialConsumer(amqpURL, messaging.TimingExchange)
	if err != nil {
		t.Fatalf("dial consumer: %v", err)
	}
	defer bus.Close()

	opts := messaging.ConsumerOptions{
		SourceExchange: messaging.DriverEventsExchange,
		QueueName:      "timing.driver-pr-updated-it",
		RoutingKeys:    []string{messaging.DriverPRUpdatedRoutingKey},
		Prefetch:       16,
		DLXExchange:    messaging.TimingDLXExchange,
	}
	consumeCtx, stop := context.WithCancel(ctx)
	defer stop()
	deliveries, err := bus.ConnectAndConsume(consumeCtx, opts, log, func(bool) {})
	if err != nil {
		t.Fatalf("connect and consume: %v", err)
	}

	hnd := &consumer.PRRefreshHandler{
		Validate:  validatorFor(t),
		Refresher: &consumer.PRStoreRefresher{Store: prStore, Now: func() string { return messaging.FormatWireTime(time.Now()) }},
		Log:       log,
		Key:       messaging.DriverPRUpdatedRoutingKey,
		Policy:    dlq.Policy{MaxAttempts: 5, BaseMs: 1000, Multiplier: 2, MaxMs: 60000},
		Retry:     bus.RetryToDLX,
		Park:      bus.ParkToDLX,
	}

	// Publish a confirmed, lower canonical PR to driver.events (after the queue is bound).
	publishDriverPRUpdated(t, amqpURL, masterID, 40000, "2026-06-05T14:02:00.000Z")

	select {
	case d := <-deliveries:
		hnd.Process(ctx, d)
	case <-time.After(15 * time.Second):
		t.Fatal("did not receive the driver.pr_updated delivery in time")
	}

	got, ok, err := prStore.Get(ctx, masterID)
	if err != nil || !ok {
		t.Fatalf("Get after refresh: ok=%v err=%v", ok, err)
	}
	if got != 40000 {
		t.Fatalf("local PR = %d after refresh, want 40000 (confirmed value wins)", got)
	}
}

// publishDriverPRUpdated publishes a contract-valid driver.pr_updated envelope to the
// driver.events exchange via a raw amqp channel (Driver's stand-in for this test).
func publishDriverPRUpdated(t *testing.T, amqpURL, masterID string, lapTimeMs int, setAt string) {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial publisher: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("publisher channel: %v", err)
	}
	defer ch.Close()
	if err := ch.ExchangeDeclare(messaging.DriverEventsExchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare driver.events: %v", err)
	}
	causation := "aa11bb22-cc33-4dd4-8ee5-ff6677889900"
	env := messaging.Envelope{
		ID:              "018f4e3c-4d5e-7f60-ab12-3c4d5e6f7081",
		Type:            messaging.DriverPRUpdatedRoutingKey,
		Source:          "driver",
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      messaging.FormatWireTime(time.Now()),
		CorrelationID:   itCorrelationID,
		CausationID:     &causation,
		Data: map[string]any{
			"masterId":  masterID,
			"lapTimeMs": lapTimeMs,
			"setAt":     setAt,
		},
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal driver.pr_updated: %v", err)
	}
	if err := ch.PublishWithContext(context.Background(), messaging.DriverEventsExchange,
		messaging.DriverPRUpdatedRoutingKey, false, false,
		amqp.Publishing{ContentType: "application/json", Body: body, DeliveryMode: amqp.Persistent}); err != nil {
		t.Fatalf("publish driver.pr_updated: %v", err)
	}
}
