//go:build integration

// Live bus-kill reconnection (Story 1.10) against a REAL RabbitMQ broker
// (testcontainers — no sleeps). Unlike outbox_integration_test (which fakes an
// outage by closing a channel), this STOPS and STARTS the broker container to
// prove the in-process reconnect supervisor: the Publisher detects the dropped
// connection, the outbox buffers new laps while down (no loss, no panic), and on
// restart the relay reconnects and flushes everything (every row sent), with the
// connection-state callback reporting the drop and the recovery.
package integration

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
)

// AC1 + AC2: a mid-session bus kill buffers laps in the outbox (no loss); on
// restore the Publisher reconnects in-process and the relay flushes every row.
func TestPublisherReconnectsAndFlushesOutboxAfterBusKill(t *testing.T) {
	container, amqpURL := startBrokerContainer(t)
	store, db, closeDB := openStore(t, filepath.Join(t.TempDir(), "timing.db"))
	defer closeDB()

	// A DURABLE capture queue bound to lap.recorded — it (and its persistent
	// messages) survives the broker Stop/Start, so we can count every lap that
	// was ever published across the bounce.
	const captureQueue = "timing.reconnect.capture.it"
	declareDurableCapture(t, amqpURL, captureQueue, lapRoutingKey)

	rec := &stateRecorder{}
	pub, err := messaging.Dial(amqpURL, messaging.TimingExchange)
	if err != nil {
		t.Fatalf("dial publisher: %v", err)
	}
	defer pub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pub.ConnectAndServe(ctx, logging.New(testWriter{t}, "timing", itCorrelationID, "error"), rec.set); err != nil {
		t.Fatalf("ConnectAndServe: %v", err)
	}
	rec.waitFor(t, true) // initial connected

	r := newOutboxRelay(t, store, pub.PublishConfirmed)
	go func() { _ = r.Run(ctx) }()

	// Publish 3 laps while healthy.
	enqueueLaps(t, store, db, 3)
	r.Kick()
	waitUntil(t, 20*time.Second, "first 3 laps sent", func() bool { return pendingCount(t, store) == 0 })

	// KILL the broker mid-session.
	stopBroker(t, container)
	rec.waitFor(t, false) // the supervisor observed the drop

	// While down: 3 more laps buffer durably in the outbox — no loss, no panic.
	enqueueLaps(t, store, db, 3)
	r.Kick()
	waitUntil(t, 10*time.Second, "3 laps buffered while down", func() bool { return pendingCount(t, store) == 3 })
	// Stable: still 3 pending a moment later (they are NOT being dropped or faked-sent).
	if got := pendingCount(t, store); got != 3 {
		t.Fatalf("pending while down = %d, want 3 (buffered, not lost/faked)", got)
	}

	// RESTORE the broker (same fixed host:port): the relay must reconnect and flush.
	startBrokerAgain(t, container)
	rec.waitForReconnect(t) // a true AFTER the observed drop
	waitUntil(t, 30*time.Second, "outbox fully flushed after reconnect", func() bool {
		r.Kick() // nudge each poll so we don't wait out a full backoff
		return pendingCount(t, store) == 0
	})

	// Every one of the 6 laps reached the durable capture queue (no loss).
	drainCount(t, amqpURL, captureQueue, 6)
}

// --- helpers (Story 1.10) ----------------------------------------------------

// startBrokerContainer runs RabbitMQ bound to a FIXED host port so the broker
// address is stable across a Stop/Start (testcontainers' default ephemeral port
// is reallocated on restart, which would point the client at a dead address — a
// harness artifact, not how a real broker restarts). The fixed port faithfully
// reproduces "the same broker comes back at the same host:port".
func startBrokerContainer(t *testing.T) (*tcrabbitmq.RabbitMQContainer, string) {
	t.Helper()
	ctx := context.Background()
	hostPort := freePort(t)
	c, err := tcrabbitmq.Run(ctx, "rabbitmq:4.3-management-alpine",
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.PortBindings = network.PortMap{
				network.MustParsePort("5672/tcp"): []network.PortBinding{{HostPort: hostPort}},
			}
		}),
	)
	if err != nil {
		t.Fatalf("start rabbitmq container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	return c, fmt.Sprintf("amqp://guest:guest@localhost:%s/", hostPort)
}

// freePort grabs an unused TCP port by binding :0 then releasing it. A small race
// (the port could be taken before the container binds it) is acceptable for a
// local/CI test and is the common testcontainers fixed-port idiom.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
}

func stopBroker(t *testing.T, c *tcrabbitmq.RabbitMQContainer) {
	t.Helper()
	d := 10 * time.Second
	if err := c.Stop(context.Background(), &d); err != nil {
		t.Fatalf("stop broker: %v", err)
	}
}

func startBrokerAgain(t *testing.T, c *tcrabbitmq.RabbitMQContainer) {
	t.Helper()
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start broker: %v", err)
	}
}

func pendingCount(t *testing.T, store *persistence.OutboxStore) int {
	t.Helper()
	pending, err := store.FetchPending(context.Background(), 1000)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	return len(pending)
}

// stateRecorder captures connection-state transitions reported by the supervisor.
type stateRecorder struct {
	mu     sync.Mutex
	states []bool
}

func (r *stateRecorder) set(connected bool) {
	r.mu.Lock()
	r.states = append(r.states, connected)
	r.mu.Unlock()
}

func (r *stateRecorder) sawSince(idx int, want bool) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := idx; i < len(r.states); i++ {
		if r.states[i] == want {
			return i + 1, true
		}
	}
	return len(r.states), false
}

func (r *stateRecorder) waitFor(t *testing.T, want bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := r.sawSince(0, want); ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for connection state %v (saw %v)", want, r.states)
}

// waitForReconnect waits for the transition sequence true -> false -> true: i.e.
// a reconnection AFTER an observed drop, not merely the initial connect.
func (r *stateRecorder) waitForReconnect(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		next, sawFalse := r.sawSince(0, false)
		if sawFalse {
			if _, sawTrueAfter := r.sawSince(next, true); sawTrueAfter {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for reconnect (true after a drop); saw %v", r.states)
}

func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition never held within %s: %s", timeout, what)
}

func declareDurableCapture(t *testing.T, amqpURL, queue, routingKey string) {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial capture: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("capture channel: %v", err)
	}
	if err := ch.ExchangeDeclare(messaging.TimingExchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare exchange: %v", err)
	}
	// durable, non-autodelete, non-exclusive → survives a broker Stop/Start.
	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		t.Fatalf("declare durable queue: %v", err)
	}
	if err := ch.QueueBind(queue, routingKey, messaging.TimingExchange, false, nil); err != nil {
		t.Fatalf("bind durable queue: %v", err)
	}
}

func drainCount(t *testing.T, amqpURL, queue string, want int) {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial drain: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("drain channel: %v", err)
	}
	got := 0
	deadline := time.Now().Add(20 * time.Second)
	for got < want && time.Now().Before(deadline) {
		msg, ok, gerr := ch.Get(queue, true)
		if gerr != nil {
			t.Fatalf("get from %s: %v", queue, gerr)
		}
		if ok {
			_ = msg
			got++
			continue
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != want {
		t.Fatalf("captured %d messages on %s, want %d", got, queue, want)
	}
}
