//go:build integration

// Live bus-kill reconnection + reconvergence (Story 1.10) for the Leaderboard
// consumer, against a REAL RabbitMQ broker (testcontainers — no sleeps). It
// STOPS/STARTS a fixed-host-port broker container to prove, against the real
// reconnect supervisor:
//
//   - AC1: on a mid-session bus kill the SERVED bundle flips to stale/reconnecting
//     and the board freezes on its last-known standings (no crash, no wipe).
//   - AC2: on restore the consumer reconnects, the board catches up, the stale
//     flag clears, and the convergence predicate holds within 10 s of broker-ready:
//     CurrentBoard == fold(the fixed lap sequence).
//   - AC3: a service (process) restart replays past the inbox marker with no
//     double-count (idempotent inbox) and no lost events (durable queue).
package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/web"
)

const reconnectQueue = "leaderboard.reconnect.it"

// lapFix is one entry in the fixed heat sequence (N=10 drivers).
type lapFix struct {
	master string
	lapMs  int64
	at     string
}

// fixedHeat builds the N=10 driver sequence: driver i sets bestMs = 40000 + i*250.
func fixedHeat(session string) []lapFix {
	out := make([]lapFix, 0, 10)
	for i := 0; i < 10; i++ {
		out = append(out, lapFix{
			master: fmt.Sprintf("0000000%d-3e84-4d11-9aa2-7b6c5e4d3f21", i),
			lapMs:  int64(40000 + i*250),
			at:     fmt.Sprintf("2026-06-08T10:00:%02d.000Z", i),
		})
	}
	return out
}

func TestLeaderboardReconnectsStaleFlagAndConverges(t *testing.T) {
	c, amqpURL := startBrokerFixedPort(t)
	store, snapshot, applied, busConnected, server, cancel := startReconnectConsumer(t, amqpURL)
	defer cancel()

	// An SSE client connected before the kill, to prove the flag is SERVED.
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	const session = "session-reconnect-it"
	heat := fixedHeat(session)

	// --- healthy: publish the first half, board converges, bundle is live.
	pub1 := dialPublisher(t, amqpURL)
	pub1.sessionStarted(t, uuid.NewString(), session, "2026-06-08T10:00:00.000Z")
	first := heat[:5]
	for _, l := range first {
		pub1.lap(t, uuid.NewString(), l.master, l.lapMs, l.at, session)
	}
	_ = pub1.close()
	waitForApplied(t, applied, len(first)+1) // +1 for session.started
	waitUntil(t, "first half converged", func() bool { return boardSize(t, store, session) == len(first) })
	if snapshot().Stale {
		t.Fatal("bundle is stale while connected")
	}

	// --- KILL the bus: the served bundle must flip to stale/reconnecting, and the
	// board must FREEZE on last-known (rows preserved, no wipe, no crash).
	stopBrokerC(t, c)
	waitUntil(t, "served bundle flips stale", func() bool { return snapshot().Stale })
	if snapshot().Connection != web.ConnectionReconnecting {
		t.Fatalf("connection = %q, want reconnecting", snapshot().Connection)
	}
	if boardSize(t, store, session) != len(first) {
		t.Fatal("stale board lost its last-known rows (must freeze, not wipe)")
	}
	// The stale flag is observable on the SERVED stream too.
	assertServedStale(t, ts.URL, true)

	// --- RESTORE the bus (same host:port). The consumer must reconnect and clear
	// the stale flag (AC2); this also confirms the broker is accepting AMQP again.
	startBrokerAgainC(t, c)
	waitUntil(t, "consumer reconnected, stale flag cleared", func() bool { return !snapshot().Stale })

	// Now publish the second half; the board must reconverge within 10 s.
	pub2 := dialPublisher(t, amqpURL)
	for _, l := range heat[5:] {
		pub2.lap(t, uuid.NewString(), l.master, l.lapMs, l.at, session)
	}
	_ = pub2.close()

	deadline := time.Now().Add(10 * time.Second) // M9: ≤10 s reconverge
	for {
		if boardSize(t, store, session) == len(heat) && !snapshot().Stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reconverge within 10s: boardSize=%d stale=%v", boardSize(t, store, session), snapshot().Stale)
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = busConnected

	// --- convergence predicate: CurrentBoard == fold(the fixed sequence).
	assertConverged(t, store, session, heat)
	assertServedStale(t, ts.URL, false) // flag cleared on the served stream
}

// AC3: a process restart replays past the inbox marker — durable read-model +
// idempotent inbox = no double-count, no loss. We consume part of the heat, then
// simulate a restart (new store + new consumer on the SAME db), redeliver an
// already-applied lap and deliver the rest, and assert exactly-once convergence.
func TestLeaderboardServiceRestartReplaysWithoutDoubleCount(t *testing.T) {
	_, amqpURL := startBrokerFixedPort(t)
	dbPath := filepath.Join(t.TempDir(), "lb-restart.db")
	const session = "session-restart-it"
	heat := fixedHeat(session)

	// envelope ids are fixed so we can REDELIVER an already-applied one after the
	// "restart" and prove the inbox dedupes it (no double-count).
	ids := make([]string, len(heat))
	for i := range heat {
		ids[i] = uuid.NewString()
	}

	// --- process 1: consume session.started + first 4 laps, then "crash".
	startedID := uuid.NewString()
	{
		store, _, applied, _, _, cancel := startReconnectConsumerDB(t, amqpURL, dbPath)
		pub := dialPublisher(t, amqpURL)
		pub.sessionStarted(t, startedID, session, "2026-06-08T10:00:00.000Z")
		for i := 0; i < 4; i++ {
			pub.lap(t, ids[i], heat[i].master, heat[i].lapMs, heat[i].at, session)
		}
		_ = pub.close()
		waitForApplied(t, applied, 5) // started + 4 laps
		waitUntil(t, "4 laps applied before restart", func() bool { return boardSize(t, store, session) == 4 })
		cancel() // process 1 stops
	}

	// --- process 2: same DB. Redeliver an already-applied lap (dedupe) + deliver
	// the rest. The read-model persisted; the inbox is the marker.
	store2, snapshot2, applied2, _, _, cancel2 := startReconnectConsumerDB(t, amqpURL, dbPath)
	defer cancel2()
	pub2 := dialPublisher(t, amqpURL)
	pub2.lap(t, ids[0], heat[0].master, heat[0].lapMs, heat[0].at, session) // duplicate of an applied lap
	for i := 4; i < len(heat); i++ {
		pub2.lap(t, ids[i], heat[i].master, heat[i].lapMs, heat[i].at, session)
	}
	_ = pub2.close()
	// 6 fresh laps apply (ids[4..9]); the duplicate is a no-op.
	waitForApplied(t, applied2, 6)
	waitUntil(t, "all laps converged after restart", func() bool { return boardSize(t, store2, session) == len(heat) })
	if snapshot2().Stale {
		t.Fatal("bundle stale after a healthy restart")
	}
	// No double-count: exactly 10 drivers, folded correctly.
	assertConverged(t, store2, session, heat)
}

// --- consumer harness ------------------------------------------------------

func startReconnectConsumer(t *testing.T, amqpURL string) (*persistence.Store, func() web.Snapshot, chan struct{}, *atomic.Bool, *web.Server, func()) {
	return startReconnectConsumerDB(t, amqpURL, filepath.Join(t.TempDir(), "lb.db"))
}

// startReconnectConsumerDB wires the REAL reconnect-aware consumer exactly as
// main does (Dial → ConnectAndConsume → Handler), against the given db path. It
// returns the store, the served-snapshot func (carries the stale flag), an
// applied-signal channel, the connection-state flag, the web server, and a cancel.
func startReconnectConsumerDB(t *testing.T, amqpURL, dbPath string) (*persistence.Store, func() web.Snapshot, chan struct{}, *atomic.Bool, *web.Server, func()) {
	t.Helper()
	dir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("resolve /contract: %v", err)
	}
	validator, err := messaging.NewValidator(dir)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db, err := persistence.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	store := persistence.NewStore(db)
	startSeq, err := store.MaxSeq(context.Background())
	if err != nil {
		t.Fatalf("max seq: %v", err)
	}

	var busConnected atomic.Bool
	busConnected.Store(true)
	snapshot := func() web.Snapshot {
		b, serr := store.CurrentBoard(context.Background())
		if serr != nil {
			t.Errorf("CurrentBoard: %v", serr)
		}
		return web.ToSnapshot(b).WithConnection(busConnected.Load())
	}
	server := web.NewServer(":0", snapshot, quietLog())

	applied := make(chan struct{}, 64)
	handler := &consumer.Handler{
		Validate: validator.ValidateEnvelopeBytes,
		Store:    store,
		Log:      quietLog(),
		Notify: func() {
			server.Publish()
			select {
			case applied <- struct{}{}:
			default:
			}
		},
		StartSeq: startSeq,
		Policy:   domain.DLQPolicy{MaxAttempts: 5, BaseMs: 50, Multiplier: 2, MaxMs: 1000},
	}

	bus, err := messaging.Dial(amqpURL, messaging.LeaderboardExchange)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	handler.Retry = bus.RetryToDLX
	handler.Park = bus.ParkToDLX

	ctx, cancel := context.WithCancel(context.Background())
	deliveries, err := bus.ConnectAndConsume(ctx, messaging.ConsumerOptions{
		SourceExchange: timingExchange,
		QueueName:      reconnectQueue,
		RoutingKeys: []string{
			messaging.LapRecordedRoutingKey,
			messaging.SessionStartedRoutingKey,
			messaging.SessionEndedRoutingKey,
		},
		Prefetch: 16,
	}, quietLog(), func(connected bool) {
		busConnected.Store(connected)
		server.Publish()
	})
	if err != nil {
		cancel()
		t.Fatalf("ConnectAndConsume: %v", err)
	}
	go handler.Run(ctx, deliveries)

	stop := func() {
		cancel()
		_ = bus.Close()
		_ = db.Close()
	}
	return store, snapshot, applied, &busConnected, server, stop
}

// --- assertions ------------------------------------------------------------

func boardSize(t *testing.T, store *persistence.Store, session string) int {
	t.Helper()
	b, err := store.CurrentBoard(context.Background())
	if err != nil {
		t.Fatalf("CurrentBoard: %v", err)
	}
	if b == nil || b.SessionID != session {
		return 0
	}
	return len(b.Bests)
}

// assertConverged checks the convergence predicate: the served read-model equals
// fold(the fixed lap sequence) — every driver's best reflected, ranked correctly.
func assertConverged(t *testing.T, store *persistence.Store, session string, heat []lapFix) {
	t.Helper()
	b, err := store.CurrentBoard(context.Background())
	if err != nil {
		t.Fatalf("CurrentBoard: %v", err)
	}
	if b == nil || b.SessionID != session {
		t.Fatalf("no board for session %q", session)
	}
	want := foldExpected(heat) // master -> best ms
	if len(b.Bests) != len(want) {
		t.Fatalf("board has %d drivers, want %d (fold mismatch)", len(b.Bests), len(want))
	}
	for _, db := range b.Bests {
		if want[db.MasterID] != db.BestLapMs {
			t.Errorf("driver %s best = %d, want %d", db.MasterID, db.BestLapMs, want[db.MasterID])
		}
	}
	// Ranking matches fold: best-lap ascending.
	ranked := domain.Rank(b.Bests)
	for i := 1; i < len(ranked); i++ {
		if ranked[i-1].BestLapMs > ranked[i].BestLapMs {
			t.Errorf("ranking not ascending at %d: %d > %d", i, ranked[i-1].BestLapMs, ranked[i].BestLapMs)
		}
	}
}

// foldExpected folds the lap sequence into each driver's best lap (the smallest).
func foldExpected(heat []lapFix) map[string]int64 {
	best := map[string]int64{}
	for _, l := range heat {
		if cur, ok := best[l.master]; !ok || l.lapMs < cur {
			best[l.master] = l.lapMs
		}
	}
	return best
}

// assertServedStale reads the initial SSE frame from the live server and asserts
// the served bundle's stale flag (proving the flag is on the SERVED stream, not
// just the in-process snapshot func).
func assertServedStale(t *testing.T, baseURL string, wantStale bool) {
	t.Helper()
	resp, err := http.Get(baseURL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	frame := readSSEFrame(t, resp.Body)
	hasReconnecting := strings.Contains(frame, `"stale":true`)
	if hasReconnecting != wantStale {
		t.Fatalf("served bundle stale=%v, want %v (frame: %s)", hasReconnecting, wantStale, frame)
	}
}

func readSSEFrame(t *testing.T, body interface{ Read([]byte) (int, error) }) string {
	t.Helper()
	buf := make([]byte, 4096)
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var acc strings.Builder
		for {
			n, err := body.Read(buf)
			if n > 0 {
				acc.Write(buf[:n])
				if strings.Contains(acc.String(), "\n\n") {
					ch <- res{acc.String(), nil}
					return
				}
			}
			if err != nil {
				ch <- res{acc.String(), err}
				return
			}
		}
	}()
	select {
	case got := <-ch:
		return got.s
	case <-time.After(8 * time.Second):
		t.Fatal("timed out reading an SSE frame")
		return ""
	}
}

// --- fixed-port broker helpers (Story 1.10) --------------------------------

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

func freeTCPPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
}
