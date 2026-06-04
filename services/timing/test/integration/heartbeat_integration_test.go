//go:build integration

// Package integration exercises the Timing skeleton against a REAL RabbitMQ
// broker (testcontainers — no sleeps; readiness asserted by the module). Run with:
//
//	go test -tags=integration ./test/integration/...
//
// Requires a reachable Docker daemon.
package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/heartbeat"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
)

// itCorrelationID is a valid lowercase UUID — the envelope schema pins
// correlationId to the UUID pattern, so a placeholder string would (correctly)
// be dropped by validate-on-publish.
const itCorrelationID = "7c9e6a55-0e42-4f8b-bd1a-3c2d1e0f9a8b"

func TestHeartbeatReachesTheBusAndValidates(t *testing.T) {
	ctx := context.Background()

	container, err := tcrabbitmq.Run(ctx, "rabbitmq:4.3-management-alpine")
	if err != nil {
		t.Fatalf("start rabbitmq container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })

	amqpURL, err := container.AmqpURL(ctx)
	if err != nil {
		t.Fatalf("amqp url: %v", err)
	}

	// Publisher = the service side (declares the durable timing.events exchange).
	pub, err := messaging.Dial(amqpURL, messaging.TimingExchange)
	if err != nil {
		t.Fatalf("dial publisher: %v", err)
	}

	// Consumer = a stand-in for the Control Room: bind a queue to timing.events
	// for control.heartbeat BEFORE the emitter starts so no early beat is missed.
	deliveries, consumerClose := bindConsumer(t, amqpURL)
	defer consumerClose()

	dir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("resolve contract dir: %v", err)
	}
	validator, err := messaging.NewValidator(dir)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	emitter := &heartbeat.Emitter{
		Interval:     200 * time.Millisecond,
		LivenessFile: t.TempDir() + "/live",
		Build: func(now time.Time) messaging.Envelope {
			return messaging.NewHeartbeatEnvelope("timing", "it-instance", itCorrelationID, now)
		},
		Validate: validator.ValidateHeartbeat,
		Publish:  pub.Publish,
		Log:      logging.New(testWriter{t}, "timing", itCorrelationID, "error"),
	}
	emitCtx, stopEmitter := context.WithCancel(ctx)
	emitterDone := make(chan struct{})
	go func() { _ = emitter.Run(emitCtx); close(emitterDone) }()

	select {
	case d := <-deliveries:
		var env messaging.Envelope
		if err := json.Unmarshal(d.Body, &env); err != nil {
			t.Fatalf("received body is not a valid envelope: %v", err)
		}
		if env.Type != messaging.HeartbeatRoutingKey {
			t.Errorf("type = %q, want %q", env.Type, messaging.HeartbeatRoutingKey)
		}
		if env.Source != "timing" {
			t.Errorf("source = %q, want timing", env.Source)
		}
		if err := validator.ValidateHeartbeat(env); err != nil {
			t.Errorf("heartbeat received off the bus failed /contract validation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat received within 5s")
	}

	// Graceful shutdown: stop the loop, then close the connection cleanly.
	stopEmitter()
	select {
	case <-emitterDone:
	case <-time.After(2 * time.Second):
		t.Fatal("emitter did not stop after cancel")
	}
	if err := pub.Close(); err != nil {
		t.Errorf("publisher did not close cleanly: %v", err)
	}
}

func bindConsumer(t *testing.T, amqpURL string) (<-chan amqp.Delivery, func()) {
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
	if err := ch.QueueBind(q.Name, messaging.HeartbeatRoutingKey, messaging.TimingExchange, false, nil); err != nil {
		t.Fatalf("bind queue: %v", err)
	}
	deliveries, err := ch.Consume(q.Name, "", true, true, false, false, nil)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	return deliveries, func() { _ = ch.Close(); _ = conn.Close() }
}

// testWriter routes the service's JSON logs into the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", p)
	return len(p), nil
}
