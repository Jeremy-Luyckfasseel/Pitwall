//go:build integration

// Identity survives a mid-flight bus kill (Story 1.10 blueprint spine) against a REAL
// fixed-host-port RabbitMQ that is STOPPED then STARTED (testcontainers — no sleeps).
// It proves the dual-role wiring's supervisors (consumer + publisher) reconnect and the
// durable store + queue carry state across the outage: after the broker is restored,
// lookups still resolve, and a known email still maps to the SAME masterId minted
// before the kill (event-carried state transfer, ADR-0002).
package integration

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
)

func TestSurvivesBusBounceAndStillResolves(t *testing.T) {
	c, amqpURL := startBrokerFixedPort(t)
	rig := startIdentity(t, amqpURL, t.TempDir()+"/identity.db")
	defer rig.stop()

	// --- healthy: mint a masterId for an email.
	obs1 := newObserver(t, amqpURL)
	fe1 := dialFrontend(t, amqpURL)
	req1 := uuid.NewString()
	fe1.lookup(t, uuid.NewString(), req1, "survivor@example.com")
	first := obs1.await(t, req1)
	obs1.close()
	fe1.close()
	if !isUUIDv4(first.MasterID) {
		t.Fatalf("pre-kill masterId %q invalid", first.MasterID)
	}

	// --- KILL the bus, then RESTORE it (same host:port; container fs preserved).
	stopBrokerC(t, c)
	startBrokerAgainC(t, c)
	waitForBroker(t, amqpURL)

	// --- after reconnect: a NEW lookup for the SAME email must resolve to the SAME
	// masterId (the identities row survived the outage; the service reconnected).
	obs2 := newObserver(t, amqpURL)
	defer obs2.close()
	fe2 := dialFrontend(t, amqpURL)
	defer fe2.close()
	req2 := uuid.NewString()
	fe2.lookup(t, uuid.NewString(), req2, "survivor@example.com")
	second := obs2.await(t, req2)
	if second.MasterID != first.MasterID {
		t.Fatalf("post-bounce masterId %q != pre-bounce %q (state lost across the outage)", second.MasterID, first.MasterID)
	}

	// --- and a brand-new email still mints (the consumer + relay are fully live again).
	req3 := uuid.NewString()
	fe2.lookup(t, uuid.NewString(), req3, "fresh-after-bounce@example.com")
	third := obs2.await(t, req3)
	if third.MasterID == first.MasterID || !isUUIDv4(third.MasterID) {
		t.Fatalf("new email after bounce should mint a fresh id; got %q", third.MasterID)
	}
	if n := rig.identityCount(t); n != 2 {
		t.Fatalf("identities = %d; want 2 (survivor + fresh)", n)
	}
}

// --- fixed-port broker helpers (mirror services/leaderboard, Story 1.10) ----

func startBrokerFixedPort(t *testing.T) (*tcrabbitmq.RabbitMQContainer, string) {
	t.Helper()
	ctx := context.Background()
	hostPort := freeTCPPort(t)
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

func stopBrokerC(t *testing.T, c *tcrabbitmq.RabbitMQContainer) {
	t.Helper()
	d := 10 * time.Second
	if err := c.Stop(context.Background(), &d); err != nil {
		t.Fatalf("stop broker: %v", err)
	}
}

func startBrokerAgainC(t *testing.T, c *tcrabbitmq.RabbitMQContainer) {
	t.Helper()
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start broker: %v", err)
	}
}

// waitForBroker blocks until the broker accepts AMQP again (post-restart warmup) —
// an observable condition, not a fixed sleep.
func waitForBroker(t *testing.T, amqpURL string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		conn, err := amqp.Dial(amqpURL)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("broker did not accept AMQP after restart: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
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
