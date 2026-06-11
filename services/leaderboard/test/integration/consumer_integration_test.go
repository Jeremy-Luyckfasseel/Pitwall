//go:build integration

// Leaderboard consumer integration (Story 1.7) against a REAL RabbitMQ broker and
// a real on-disk SQLite database (testcontainers — no sleeps). Proves the slice
// end-to-end: laps published to timing.events are consumed, deduped by envelope
// id (a redelivered lap is a no-op), folded into a best-lap projection ordered
// ascending with the first-to-set tie-break, and pushed to a connected SSE client.
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/web"
)

const (
	timingExchange = "timing.events"
	driverA        = "aaaaaaaa-3e84-4d11-9aa2-7b6c5e4d3f21"
	driverB        = "bbbbbbbb-3e84-4d11-9aa2-7b6c5e4d3f21"
)

func quietLog() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// TestLeaderboardConsumesLapsEndToEnd is the walking-skeleton slice: Timing →
// bus → Leaderboard → standings → SSE. The crafted sequence exercises dedupe,
// best-lap-ascending order, and the equal-best first-to-set tie-break.
func TestLeaderboardConsumesLapsEndToEnd(t *testing.T) {
	amqpURL := startBroker(t)

	// --- the consuming service: real validator + real SQLite store + SSE server.
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
	defer func() { _ = db.Close() }()
	store := persistence.NewStore(db)

	snapshot := func() web.Snapshot {
		b, serr := store.CurrentBoard(context.Background())
		if serr != nil {
			t.Errorf("CurrentBoard: %v", serr)
		}
		return web.ToSnapshot(b)
	}
	server := web.NewServer(":0", snapshot, quietLog())

	// Notify hook: push to SSE AND signal the test (observable condition — we wait
	// for the expected number of APPLIED laps, never time.Sleep).
	applied := make(chan struct{}, 16)
	handler := &consumer.Handler{
		Validate: validator.ValidateEnvelopeBytes,
		Store:    store,
		Log:      quietLog(),
		Notify: func() {
			server.Publish()
			applied <- struct{}{}
		},
	}

	bus, err := messaging.Dial(amqpURL, messaging.LeaderboardExchange)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer func() { _ = bus.Close() }()
	if err := bus.DeclareConsumerQueue(messaging.ConsumerOptions{
		SourceExchange: timingExchange,
		QueueName:      "leaderboard.lap-recorded.it",
		RoutingKeys:    []string{messaging.LapRecordedRoutingKey},
		Prefetch:       16,
	}); err != nil {
		t.Fatalf("declare consumer queue: %v", err)
	}
	deliveries, err := bus.Consume("leaderboard.lap-recorded.it")
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handler.Run(ctx, deliveries)

	// --- an SSE client connected BEFORE the laps flow (AC4).
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	sse := bufio.NewReader(resp.Body)
	_ = mustReadEvent(t, sse) // initial (empty) snapshot

	// --- the producer (Timing): publish the crafted lap sequence to timing.events.
	pub := dialPublisher(t, amqpURL)
	defer func() { _ = pub.close() }()

	// A sets 42.000 first; the SAME envelope id is redelivered (dedupe no-op); B
	// matches 42.000 later (ranks below A by the first-to-set tie-break); A then
	// improves to 41.000. Expected APPLIED laps: A1, B1, A2 = 3 (the duplicate of
	// A1 must NOT apply).
	// Envelope ids MUST be canonical lowercase UUIDs (envelope schema) — else
	// validate-on-consume rejects them. The A1 id repeats to exercise dedupe.
	const idA1 = "11111111-1111-7111-8111-111111111111"
	pub.lap(t, idA1, driverA, 42000, "2026-06-08T10:00:01.000Z")
	pub.lap(t, idA1, driverA, 42000, "2026-06-08T10:00:01.000Z") // redelivery (same id)
	pub.lap(t, "22222222-2222-7222-8222-222222222222", driverB, 42000, "2026-06-08T10:00:05.000Z")
	pub.lap(t, "33333333-3333-7333-8333-333333333333", driverA, 41000, "2026-06-08T10:00:09.000Z")

	waitForApplied(t, applied, 3)

	// --- assert the converged projection (dedupe + order + tie-break).
	b, err := store.CurrentBoard(context.Background())
	if err != nil {
		t.Fatalf("CurrentBoard: %v", err)
	}
	if b == nil {
		t.Fatal("no current board after applied laps")
	}
	bests := b.Bests
	if len(bests) != 2 {
		t.Fatalf("standings has %d drivers, want 2 (the redelivered lap must not add one)", len(bests))
	}
	ranked := domain.Rank(bests)
	if ranked[0].MasterID != driverA || ranked[0].BestLapMs != 41000 {
		t.Errorf("rank 1 = %s @%d, want driverA @41000", ranked[0].MasterID, ranked[0].BestLapMs)
	}
	if ranked[1].MasterID != driverB || ranked[1].BestLapMs != 42000 {
		t.Errorf("rank 2 = %s @%d, want driverB @42000", ranked[1].MasterID, ranked[1].BestLapMs)
	}
	if !ranked[0].IsFastest || ranked[1].IsFastest {
		t.Errorf("only rank 1 should be flagged fastest")
	}

	// --- assert the SSE stream delivered the converged standings (AC4).
	deadline := time.After(8 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("SSE client never received the converged standings")
		default:
		}
		data := mustReadEvent(t, sse)
		if strings.Contains(data, "41000") && strings.Contains(data, driverA) {
			var snap web.Snapshot
			if err := json.Unmarshal([]byte(data), &snap); err != nil {
				t.Fatalf("SSE frame not valid JSON: %v", err)
			}
			if len(snap.Rows) == 2 && snap.Rows[0].MasterID == driverA && snap.Rows[0].IsFastest {
				return // converged, ordered, fastest-flagged — proven over SSE.
			}
		}
	}
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

func waitForApplied(t *testing.T, applied <-chan struct{}, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-applied:
		case <-time.After(15 * time.Second):
			t.Fatalf("only %d of %d laps were applied within the deadline", i, n)
		}
	}
}

func mustReadEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var data []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				ch <- res{"", err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if len(data) > 0 {
					ch <- res{strings.Join(data, "\n"), nil}
					return
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("read SSE event: %v", got.err)
		}
		return got.s
	case <-time.After(8 * time.Second):
		t.Fatal("timed out reading an SSE event")
		return ""
	}
}

// publisher is a minimal Timing stand-in: it declares timing.events and publishes
// lap.recorded envelopes.
type publisher struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

func dialPublisher(t *testing.T, amqpURL string) *publisher {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial publisher: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("publisher channel: %v", err)
	}
	if err := ch.ExchangeDeclare(timingExchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare timing.events: %v", err)
	}
	return &publisher{conn: conn, ch: ch}
}

func (p *publisher) lap(t *testing.T, id, master string, lapMs int64, at string) {
	t.Helper()
	env := messaging.Envelope{
		ID:              id,
		Type:            messaging.LapRecordedRoutingKey,
		Source:          "timing",
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      at,
		CorrelationID:   "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		CausationID:     nil,
		Data: messaging.LapRecordedData{
			MasterID:  master,
			SessionID: "session-it",
			LapNumber: 3,
			LapTimeMs: lapMs,
			At:        at,
		},
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal lap: %v", err)
	}
	if err := p.ch.PublishWithContext(context.Background(), timingExchange,
		messaging.LapRecordedRoutingKey, false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		}); err != nil {
		t.Fatalf("publish lap: %v", err)
	}
}

func (p *publisher) close() error {
	_ = p.ch.Close()
	return p.conn.Close()
}
