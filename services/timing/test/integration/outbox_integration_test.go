//go:build integration

// Outbox + relay integration (Story 1.4) against a REAL RabbitMQ broker and a
// real on-disk SQLite database (testcontainers — no sleeps). Proves the three
// reliability properties end-to-end: publish+mark-sent, survive a broker outage
// and publish on recovery, and replay durable rows across a restart.
package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/relay"
)

const lapRoutingKey = "lap.recorded"

// AC1: an enqueued event publishes to timing.events and its row flips to sent
// only after the broker confirm.
func TestOutboxRelayPublishesAndMarksSent(t *testing.T) {
	amqpURL := startBroker(t)
	store, db, closeDB := openStore(t, filepath.Join(t.TempDir(), "timing.db"))
	defer closeDB()

	deliveries, closeConsumer := bindConsumerKey(t, amqpURL, lapRoutingKey)
	defer closeConsumer()

	pub, confirmCh := dialConfirm(t, amqpURL)
	defer pub.Close()
	defer confirmCh.Close()

	ids := enqueueLaps(t, store, db, 2)

	r := newOutboxRelay(t, store, confirmCh.PublishConfirmed)
	sent, err := r.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if sent != 2 {
		t.Fatalf("sent = %d, want 2", sent)
	}

	validate := validatorFor(t)
	for i := 0; i < 2; i++ {
		select {
		case d := <-deliveries:
			var env messaging.Envelope
			if err := json.Unmarshal(d.Body, &env); err != nil {
				t.Fatalf("delivery not a valid envelope: %v", err)
			}
			if env.Type != lapRoutingKey {
				t.Errorf("type = %q, want %q", env.Type, lapRoutingKey)
			}
			if err := validate(d.Body); err != nil {
				t.Errorf("event off the bus failed /contract validation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("only received %d of 2 events", i)
		}
	}
	assertAllSent(t, store)
	_ = ids
}

// AC3: a broker outage keeps rows durably pending (no loss); when the broker is
// reachable again they publish.
func TestOutboxSurvivesBrokerOutageAndPublishesOnRecovery(t *testing.T) {
	amqpURL := startBroker(t)
	store, db, closeDB := openStore(t, filepath.Join(t.TempDir(), "timing.db"))
	defer closeDB()

	enqueueLaps(t, store, db, 3)

	// "Outage": a confirm channel that is closed → every publish fails.
	pubDown, downCh := dialConfirm(t, amqpURL)
	_ = downCh.Close() // break it
	rDown := newOutboxRelay(t, store, downCh.PublishConfirmed)
	if _, err := rDown.DrainOnce(context.Background()); err == nil {
		t.Fatalf("expected publish to fail during the simulated outage")
	}
	_ = pubDown.Close()

	// No loss: all rows are still pending after the failed publish.
	pending, err := store.FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("pending after outage = %d, want 3 (no loss)", len(pending))
	}

	// "Recovery": a healthy channel publishes the buffered rows.
	deliveries, closeConsumer := bindConsumerKey(t, amqpURL, lapRoutingKey)
	defer closeConsumer()
	pubUp, upCh := dialConfirm(t, amqpURL)
	defer pubUp.Close()
	defer upCh.Close()

	rUp := newOutboxRelay(t, store, upCh.PublishConfirmed)
	sent, err := rUp.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce on recovery: %v", err)
	}
	if sent != 3 {
		t.Fatalf("published on recovery = %d, want 3", sent)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-deliveries:
		case <-time.After(5 * time.Second):
			t.Fatalf("only received %d of 3 events on recovery", i)
		}
	}
	assertAllSent(t, store)
}

// AC3 (restart half): rows committed to the outbox survive a process restart
// (same DB file) and publish on the next start.
func TestOutboxRowsSurviveRestart(t *testing.T) {
	amqpURL := startBroker(t)
	dbPath := filepath.Join(t.TempDir(), "timing.db")

	// First "process": enqueue, then close the DB without publishing.
	store1, db1, closeDB1 := openStore(t, dbPath)
	enqueueLaps(t, store1, db1, 2)
	closeDB1()

	// Second "process": reopen the same file — rows are still pending.
	store2, _, closeDB2 := openStore(t, dbPath)
	defer closeDB2()
	pending, err := store2.FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("FetchPending after restart: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending after restart = %d, want 2 (durable across restart)", len(pending))
	}

	deliveries, closeConsumer := bindConsumerKey(t, amqpURL, lapRoutingKey)
	defer closeConsumer()
	pub, confirmCh := dialConfirm(t, amqpURL)
	defer pub.Close()
	defer confirmCh.Close()

	r := newOutboxRelay(t, store2, confirmCh.PublishConfirmed)
	if _, err := r.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce after restart: %v", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-deliveries:
		case <-time.After(5 * time.Second):
			t.Fatalf("only replayed %d of 2 events after restart", i)
		}
	}
	assertAllSent(t, store2)
}

// --- helpers -------------------------------------------------------------

func startBroker(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	container, err := tcrabbitmq.Run(ctx, "rabbitmq:4.3-management-alpine")
	if err != nil {
		t.Fatalf("start rabbitmq container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	url, err := container.AmqpURL(ctx)
	if err != nil {
		t.Fatalf("amqp url: %v", err)
	}
	return url
}

func openStore(t *testing.T, dbPath string) (*persistence.OutboxStore, *sql.DB, func()) {
	t.Helper()
	db, err := persistence.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return persistence.NewOutboxStore(db), db, func() { _ = db.Close() }
}

func newOutboxRelay(t *testing.T, store relay.Store, publish relay.Publisher) *relay.Relay {
	t.Helper()
	return relay.New(relay.Config{
		Store:    store,
		Validate: validatorFor(t),
		Publish:  publish,
		Interval: 100 * time.Millisecond,
		Log:      logging.New(testWriter{t}, "timing", itCorrelationID, "error"),
	})
}

func validatorFor(t *testing.T) func([]byte) error {
	t.Helper()
	dir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("resolve contract dir: %v", err)
	}
	v, err := messaging.NewValidator(dir)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	return v.ValidateEnvelopeBytes
}

func lapEnvelope() messaging.Envelope {
	now := time.Now()
	return messaging.Envelope{
		ID:              uuid.NewString(),
		Type:            lapRoutingKey,
		Source:          "timing",
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      messaging.FormatWireTime(now),
		CorrelationID:   itCorrelationID,
		CausationID:     nil,
		Data: map[string]any{
			"masterId":  "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21",
			"sessionId": "session-it",
			"lapNumber": 7,
			"lapTimeMs": 42318,
			"at":        messaging.FormatWireTime(now),
		},
	}
}

func enqueueLaps(t *testing.T, store *persistence.OutboxStore, db *sql.DB, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		env := lapEnvelope()
		ids = append(ids, env.ID)
		if err := persistence.WithinTx(context.Background(), db, func(tx *sql.Tx) error {
			return relay.EnqueueEnvelope(tx, store, validatorFor(t), env)
		}); err != nil {
			t.Fatalf("enqueue lap %d: %v", i, err)
		}
	}
	return ids
}

func assertAllSent(t *testing.T, store *persistence.OutboxStore) {
	t.Helper()
	pending, err := store.FetchPending(context.Background(), 100)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("%d rows still pending, want all sent", len(pending))
	}
}

func dialConfirm(t *testing.T, amqpURL string) (*messaging.Publisher, *messaging.ConfirmChannel) {
	t.Helper()
	pub, err := messaging.Dial(amqpURL, messaging.TimingExchange)
	if err != nil {
		t.Fatalf("dial publisher: %v", err)
	}
	ch, err := pub.OpenConfirmChannel()
	if err != nil {
		t.Fatalf("open confirm channel: %v", err)
	}
	return pub, ch
}

func bindConsumerKey(t *testing.T, amqpURL, routingKey string) (<-chan amqp.Delivery, func()) {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial consumer: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("consumer channel: %v", err)
	}
	if err := ch.ExchangeDeclare(messaging.TimingExchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare exchange: %v", err)
	}
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatalf("declare queue: %v", err)
	}
	if err := ch.QueueBind(q.Name, routingKey, messaging.TimingExchange, false, nil); err != nil {
		t.Fatalf("bind queue: %v", err)
	}
	deliveries, err := ch.Consume(q.Name, "", true, true, false, false, nil)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	return deliveries, func() { _ = ch.Close(); _ = conn.Close() }
}
