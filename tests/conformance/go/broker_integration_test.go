//go:build integration

package conformance

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
)

// broker is the single shared RabbitMQ every scenario runs against. It is pinned
// to a FIXED host port so a state-preserving Stop->Start returns the SAME
// broker at the SAME host:port (Story 1.10 proved testcontainers reallocates the
// ephemeral port on Start, which would point clients at a dead address). The
// fixed port + state-preserving Stop is the mechanism behind peer-down /
// publish-redeliver / crash-after-ack.
type broker struct {
	c       *tcrabbitmq.RabbitMQContainer
	amqpURL string
	port    string
}

func startBroker(t *testing.T) *broker {
	t.Helper()
	ctx := context.Background()
	port := freeTCPPort(t)
	c, err := tcrabbitmq.Run(ctx, "rabbitmq:4.3-management-alpine",
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.PortBindings = network.PortMap{
				network.MustParsePort("5672/tcp"): []network.PortBinding{{HostPort: port}},
			}
		}),
	)
	if err != nil {
		t.Fatalf("start rabbitmq container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	return &broker{c: c, amqpURL: fmt.Sprintf("amqp://guest:guest@localhost:%s/", port), port: port}
}

// stop performs a state-preserving stop (durable exchanges/queues + persistent
// messages survive) — this is "the bus went down", not "the bus was destroyed".
func (b *broker) stop(t *testing.T) {
	t.Helper()
	d := 10 * time.Second
	if err := b.c.Stop(context.Background(), &d); err != nil {
		t.Fatalf("stop broker: %v", err)
	}
}

// start brings the same broker back at the same host:port. testcontainers only
// re-applies its log-readiness wait on the initial Run (not on Start), so we poll
// a real AMQP dial until the broker is actually accepting connections — otherwise
// a client that dials immediately races the still-booting broker (501 EOF).
func (b *broker) start(t *testing.T) {
	t.Helper()
	if err := b.c.Start(context.Background()); err != nil {
		t.Fatalf("start broker: %v", err)
	}
	b.waitDialable(t)
}

// waitDialable blocks until a fresh AMQP connection to the broker succeeds.
func (b *broker) waitDialable(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, err := amqp.Dial(b.amqpURL)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("broker not accepting AMQP after restart: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
}
