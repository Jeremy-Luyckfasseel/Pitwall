//go:build integration

// Leaderboard DLQ integration (Story 1.9) against a REAL RabbitMQ broker
// (testcontainers — no sleeps) and a real SQLite store. Proves the consumer-side
// poison-message path end to end: a transient failure is redelivered after the
// backoff TTL and ultimately applied (AC1); a genuine poison message exhausts the
// retry cap and lands in the terminal parking quarantine + emits the alert (AC2);
// an invalid-on-consume message is parked IMMEDIATELY, bypassing the retry queue
// (AC3). A COMPRESSED backoff policy keeps the suite fast — the mechanism is
// proven here; the production 1 s schedule is proven by the domain/config units.
package integration

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/persistence"
)

// compressedPolicy: 5 attempts, 50 ms base ×2 (50/100/200/400 ms ≈ 0.75 s total)
// — the production shape at 1/20th the wall-clock.
var compressedPolicy = domain.DLQPolicy{MaxAttempts: 5, BaseMs: 50, Multiplier: 2, MaxMs: 60000}

// faultApplier wraps a real store and induces processing failures: failsLeft > 0
// fails that many applies then delegates; failsLeft < 0 fails forever.
type faultApplier struct {
	inner     *persistence.Store
	mu        sync.Mutex
	failsLeft int
}

func (f *faultApplier) shouldFail() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failsLeft == 0 {
		return false
	}
	if f.failsLeft > 0 {
		f.failsLeft--
	}
	return true
}

var errInduced = errors.New("induced processing failure")

func (f *faultApplier) ApplyLap(ctx context.Context, id, typ, at, sid string, lap domain.Lap) (bool, bool, error) {
	if f.shouldFail() {
		return false, false, errInduced
	}
	return f.inner.ApplyLap(ctx, id, typ, at, sid, lap)
}
func (f *faultApplier) ApplySessionStarted(ctx context.Context, id, typ, at, sid, startedAt string) (bool, bool, error) {
	if f.shouldFail() {
		return false, false, errInduced
	}
	return f.inner.ApplySessionStarted(ctx, id, typ, at, sid, startedAt)
}
func (f *faultApplier) ApplySessionEnded(ctx context.Context, id, typ, at, sid, endedAt string) (bool, bool, error) {
	if f.shouldFail() {
		return false, false, errInduced
	}
	return f.inner.ApplySessionEnded(ctx, id, typ, at, sid, endedAt)
}

// syncBuffer is a concurrency-safe log sink so the test can assert the alert line
// while the consumer goroutine writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// dlqConsumer bundles the running consumer + its queue names for assertions.
type dlqConsumer struct {
	bus      *messaging.Bus
	store    *persistence.Store
	work     string
	retryQ   string
	parkingQ string
}

// startDLQConsumer declares the full DLQ topology, starts the consumer over the
// given fault applier with the compressed policy, and returns the handles.
func startDLQConsumer(t *testing.T, amqpURL, queueName string, fault *faultApplier, logger *slog.Logger) *dlqConsumer {
	t.Helper()
	dir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("resolve /contract: %v", err)
	}
	validator, err := messaging.NewValidator(dir)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db, err := persistence.Open(context.Background(), filepath.Join(t.TempDir(), "lb.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := persistence.NewStore(db)
	fault.inner = store

	bus, err := messaging.Dial(amqpURL, messaging.LeaderboardExchange)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	if err := bus.DeclareDLQTopology(messaging.ConsumerOptions{
		SourceExchange: timingExchange,
		QueueName:      queueName,
		RoutingKeys: []string{
			messaging.LapRecordedRoutingKey,
			messaging.SessionStartedRoutingKey,
			messaging.SessionEndedRoutingKey,
		},
		Prefetch: 16,
	}); err != nil {
		t.Fatalf("declare DLQ topology: %v", err)
	}
	deliveries, err := bus.Consume(queueName)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	handler := &consumer.Handler{
		Validate: validator.ValidateEnvelopeBytes,
		Store:    fault,
		Log:      logger,
		Policy:   compressedPolicy,
		Retry:    bus.RetryToDLX,
		Park:     bus.ParkToDLX,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go handler.Run(ctx, deliveries)

	return &dlqConsumer{
		bus:      bus,
		store:    store,
		work:     queueName,
		retryQ:   messaging.RetryQueueName(queueName),
		parkingQ: messaging.ParkingQueueName(queueName),
	}
}

// inspectChannel opens a dedicated channel for draining/inspecting queues.
func inspectChannel(t *testing.T, amqpURL string) *amqp.Channel {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial inspect: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("inspect channel: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close(); _ = conn.Close() })
	return ch
}

// getOne pulls (and acks) one message from a queue, if any.
func getOne(t *testing.T, ch *amqp.Channel, queue string) (amqp.Delivery, bool) {
	t.Helper()
	msg, ok, err := ch.Get(queue, true)
	if err != nil {
		t.Fatalf("get %s: %v", queue, err)
	}
	return msg, ok
}

// publishRaw publishes arbitrary bytes (used for a contract-invalid message).
func publishRaw(t *testing.T, p *publisher, routingKey string, body []byte) {
	t.Helper()
	if err := p.ch.PublishWithContext(context.Background(), timingExchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	}); err != nil {
		t.Fatalf("publish raw: %v", err)
	}
}

// TestDLQ_TransientFailure_RetriedThenApplied (AC1): the first apply fails; the
// message is redelivered from the retry queue after the backoff TTL and applied
// exactly once (idempotent inbox), so the board converges.
func TestDLQ_TransientFailure_RetriedThenApplied(t *testing.T) {
	amqpURL := startBroker(t)
	c := startDLQConsumer(t, amqpURL, "leaderboard.dlq-retry.it", &faultApplier{failsLeft: 1}, quietLog())

	pub := dialPublisher(t, amqpURL)
	defer func() { _ = pub.close() }()
	pub.lap(t, "dd000001-0000-7000-8000-00000000d001", driverA, 42000, "2026-06-08T10:00:01.000Z", "sess-dlq")

	waitUntil(t, "lap applied after a transient failure + retry", func() bool {
		b, err := c.store.CurrentBoard(context.Background())
		return err == nil && b != nil && len(b.Bests) == 1 && b.Bests[0].BestLapMs == 42000
	})
}

// TestDLQ_Poison_ParkedAndAlerted (AC2): an always-failing apply exhausts the cap
// and the message is moved to the terminal parking queue (no further requeue),
// the alert line is emitted, and the read-model is never touched.
func TestDLQ_Poison_ParkedAndAlerted(t *testing.T) {
	amqpURL := startBroker(t)
	var logBuf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	c := startDLQConsumer(t, amqpURL, "leaderboard.dlq-poison.it", &faultApplier{failsLeft: -1}, logger)
	inspect := inspectChannel(t, amqpURL)

	pub := dialPublisher(t, amqpURL)
	defer func() { _ = pub.close() }()
	pub.lap(t, "dd000002-0000-7000-8000-00000000d002", driverA, 42000, "2026-06-08T10:00:01.000Z", "sess-dlq")

	var parked amqp.Delivery
	waitUntil(t, "poison message parked after exhausting retries", func() bool {
		msg, ok := getOne(t, inspect, c.parkingQ)
		if ok {
			parked = msg
			return true
		}
		return false
	})
	if got := parked.Headers[messaging.ParkReasonHeader]; got != "retries-exhausted" {
		t.Errorf("park reason header = %v, want retries-exhausted", got)
	}
	waitUntil(t, "Control-Room alert emitted", func() bool {
		return bytes.Contains([]byte(logBuf.String()), []byte("message_parked"))
	})
	if b, _ := c.store.CurrentBoard(context.Background()); b != nil {
		t.Errorf("a poison message must never reach the read-model: %+v", b)
	}
	// And it is NOT requeued anywhere: retry + work are drained.
	if _, ok := getOne(t, inspect, c.retryQ); ok {
		t.Error("retry queue should be empty after parking (no further requeue)")
	}
}

// TestDLQ_Invalid_ParkedImmediately (AC3): a contract-invalid message is parked
// straight away with reason contract-invalid and NEVER enters the retry queue —
// 100 % of invalid messages captured (M5), none retried as poison.
func TestDLQ_Invalid_ParkedImmediately(t *testing.T) {
	amqpURL := startBroker(t)
	c := startDLQConsumer(t, amqpURL, "leaderboard.dlq-invalid.it", &faultApplier{failsLeft: 0}, quietLog())
	inspect := inspectChannel(t, amqpURL)

	dir, _ := messaging.ResolveContractDir("")
	invalid, err := os.ReadFile(filepath.Join(dir, "examples/timing/lap.recorded.v1.invalid.json"))
	if err != nil {
		t.Fatalf("read invalid fixture: %v", err)
	}

	pub := dialPublisher(t, amqpURL)
	defer func() { _ = pub.close() }()
	publishRaw(t, pub, messaging.LapRecordedRoutingKey, invalid)

	var parked amqp.Delivery
	waitUntil(t, "invalid message parked immediately", func() bool {
		msg, ok := getOne(t, inspect, c.parkingQ)
		if ok {
			parked = msg
			return true
		}
		return false
	})
	if got := parked.Headers[messaging.ParkReasonHeader]; got != "contract-invalid" {
		t.Errorf("park reason header = %v, want contract-invalid", got)
	}
	// It must NOT have been routed through the retry queue (not retried as poison).
	if _, ok := getOne(t, inspect, c.retryQ); ok {
		t.Error("an invalid message must bypass the retry queue (parked immediately)")
	}
}
