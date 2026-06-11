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
	pub.lap(t, idA1, driverA, 42000, "2026-06-08T10:00:01.000Z", "session-it")
	pub.lap(t, idA1, driverA, 42000, "2026-06-08T10:00:01.000Z", "session-it") // redelivery (same id)
	pub.lap(t, "22222222-2222-7222-8222-222222222222", driverB, 42000, "2026-06-08T10:00:05.000Z", "session-it")
	pub.lap(t, "33333333-3333-7333-8333-333333333333", driverA, 41000, "2026-06-08T10:00:09.000Z", "session-it")

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

// TestSessionLifecycleEndToEnd is Story 1.8's slice (AC1+AC2+AC3): real
// session.started / lap.recorded / session.ended envelopes (the shapes Timing's
// simulator emits through the outbox since 1.5) flow over a real broker into the
// session-keyed read-model.
//
//  1. lifecycle/auto-reset: session A starts (active) → laps → ends (finished,
//     final standings stay); session B starts → the board shows ONLY B (FR43/FR45).
//  2. out-of-order: a lap for session C arrives BEFORE C's start → implicit
//     board; the late start reconciles it without touching the lap (NFR24).
//  3. replay: A's original start envelope (same id → inbox no-op), a fresh-id
//     start for finished A (gating no-op), and a fresh-id start for live C
//     (idempotent upsert) — none wipes or reorders the live board.
//
// All waits are on observable conditions (store predicates / SSE frames) — no
// bare sleeps.
func TestSessionLifecycleEndToEnd(t *testing.T) {
	amqpURL := startBroker(t)

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
	handler := &consumer.Handler{
		Validate: validator.ValidateEnvelopeBytes,
		Store:    store,
		Log:      quietLog(),
		Notify:   server.Publish,
	}

	bus, err := messaging.Dial(amqpURL, messaging.LeaderboardExchange)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer func() { _ = bus.Close() }()
	if err := bus.DeclareConsumerQueue(messaging.ConsumerOptions{
		SourceExchange: timingExchange,
		QueueName:      "leaderboard.session-lifecycle.it",
		RoutingKeys: []string{
			messaging.LapRecordedRoutingKey,
			messaging.SessionStartedRoutingKey,
			messaging.SessionEndedRoutingKey,
		},
		Prefetch: 16,
	}); err != nil {
		t.Fatalf("declare consumer queue: %v", err)
	}
	deliveries, err := bus.Consume("leaderboard.session-lifecycle.it")
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handler.Run(ctx, deliveries)

	// SSE client connected before anything flows.
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	sse := bufio.NewReader(resp.Body)
	_ = mustReadEvent(t, sse) // initial waiting-state snapshot

	pub := dialPublisher(t, amqpURL)
	defer func() { _ = pub.close() }()

	boardIs := func(sessionID, status string, rowCount int) func() bool {
		return func() bool {
			b, berr := store.CurrentBoard(context.Background())
			if berr != nil || b == nil {
				return false
			}
			return b.SessionID == sessionID && b.Status == status && len(b.Bests) == rowCount
		}
	}

	// --- 1) lifecycle + auto-reset (AC1 + AC2) -----------------------------
	const startA = "aaaa0001-0000-7000-8000-00000000000a"
	pub.sessionStarted(t, startA, "sess-A", "2026-06-08T10:00:00.000Z")
	waitUntil(t, "session A active", boardIs("sess-A", persistence.StatusActive, 0))

	pub.lap(t, "aaaa0002-0000-7000-8000-00000000000a", driverA, 42000, "2026-06-08T10:00:43.000Z", "sess-A")
	pub.lap(t, "aaaa0003-0000-7000-8000-00000000000a", driverB, 43000, "2026-06-08T10:00:44.000Z", "sess-A")
	waitUntil(t, "two laps on board A", boardIs("sess-A", persistence.StatusActive, 2))

	pub.sessionEnded(t, "aaaa0004-0000-7000-8000-00000000000a", "sess-A", "2026-06-08T10:20:00.000Z")
	waitUntil(t, "session A finished with final standings up", boardIs("sess-A", persistence.StatusFinished, 2))

	// The status flip reaches a connected SSE client (FR45 on the live board).
	waitForSSEFrame(t, sse, func(frame string) bool {
		return strings.Contains(frame, `"sessionId":"sess-A"`) && strings.Contains(frame, `"status":"finished"`)
	})

	// New session: the board AUTO-RESETS — only B's rows are served (FR43).
	pub.sessionStarted(t, "bbbb0001-0000-7000-8000-00000000000b", "sess-B", "2026-06-08T11:00:00.000Z")
	pub.lap(t, "bbbb0002-0000-7000-8000-00000000000b", driverA, 45000, "2026-06-08T11:00:45.000Z", "sess-B")
	waitUntil(t, "board reset to session B with only B's lap", boardIs("sess-B", persistence.StatusActive, 1))
	b, _ := store.CurrentBoard(context.Background())
	if b.Bests[0].BestLapMs != 45000 {
		t.Errorf("B board best = %d, want 45000 (A's 42000 must not leak into B)", b.Bests[0].BestLapMs)
	}
	waitForSSEFrame(t, sse, func(frame string) bool {
		return strings.Contains(frame, `"sessionId":"sess-B"`) && strings.Contains(frame, `"status":"active"`)
	})

	// --- 2) out-of-order: lap before its session.started (AC3) -------------
	pub.lap(t, "cccc0001-0000-7000-8000-00000000000c", driverA, 41000, "2026-06-08T12:00:41.000Z", "sess-C")
	waitUntil(t, "implicit board for session C", boardIs("sess-C", persistence.StatusImplicit, 1))

	const startC = "cccc0002-0000-7000-8000-00000000000c"
	pub.sessionStarted(t, startC, "sess-C", "2026-06-08T12:00:00.000Z")
	waitUntil(t, "late start reconciles C to active, lap intact", boardIs("sess-C", persistence.StatusActive, 1))

	// --- 3) replays never wipe a live board (AC3) ---------------------------
	pub.sessionStarted(t, startA, "sess-A", "2026-06-08T10:00:00.000Z")                                 // same envelope id → inbox no-op
	pub.sessionStarted(t, "aaaa0009-0000-7000-8000-00000000000a", "sess-A", "2026-06-08T10:00:00.000Z") // fresh id, finished session → gating no-op
	pub.sessionStarted(t, "cccc0009-0000-7000-8000-00000000000c", "sess-C", "2026-06-08T12:00:00.000Z") // fresh id, live session → idempotent upsert
	// Sentinel: a later lap on C — the single ordered queue guarantees the three
	// replays above were fully processed once this lap is visible.
	pub.lap(t, "cccc0003-0000-7000-8000-00000000000c", driverA, 40000, "2026-06-08T12:01:30.000Z", "sess-C")
	waitUntil(t, "sentinel lap applied after the replays", func() bool {
		cur, cerr := store.CurrentBoard(context.Background())
		return cerr == nil && cur != nil && cur.SessionID == "sess-C" && len(cur.Bests) == 1 && cur.Bests[0].BestLapMs == 40000
	})

	cur, _ := store.CurrentBoard(context.Background())
	if cur.Status != persistence.StatusActive {
		t.Errorf("live board status after replays = %q, want active (no reopen/wipe)", cur.Status)
	}
	// The finished session A survives untouched (replay reconciled, not wiped).
	var statusA string
	if err := db.QueryRow(`SELECT status FROM sessions WHERE session_id = 'sess-A'`).Scan(&statusA); err != nil {
		t.Fatalf("query sess-A: %v", err)
	}
	if statusA != persistence.StatusFinished {
		t.Errorf("sess-A status after replayed start = %q, want finished (forward-only)", statusA)
	}
	var rowsA int
	if err := db.QueryRow(`SELECT COUNT(*) FROM standings WHERE session_id = 'sess-A'`).Scan(&rowsA); err != nil {
		t.Fatalf("count sess-A standings: %v", err)
	}
	if rowsA != 2 {
		t.Errorf("sess-A has %d standings rows after replays, want 2 (never wiped)", rowsA)
	}
}

// --- helpers -------------------------------------------------------------

// waitUntil polls an observable predicate until it holds (or fails the test) —
// the no-sleep discipline: we wait on state, not on time.
func waitUntil(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if pred() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(25 * time.Millisecond) // poll interval, not a timing assumption
	}
}

// waitForSSEFrame reads pushed frames until one matches (or times out via
// mustReadEvent's own deadline).
func waitForSSEFrame(t *testing.T, r *bufio.Reader, match func(string) bool) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("SSE client never received the expected frame")
		default:
		}
		if match(mustReadEvent(t, r)) {
			return
		}
	}
}

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

func (p *publisher) lap(t *testing.T, id, master string, lapMs int64, at string, sessionID string) {
	t.Helper()
	p.publish(t, id, messaging.LapRecordedRoutingKey, at, messaging.LapRecordedData{
		MasterID:  master,
		SessionID: sessionID,
		LapNumber: 3,
		LapTimeMs: lapMs,
		At:        at,
	})
}

func (p *publisher) sessionStarted(t *testing.T, id, sessionID, startedAt string) {
	t.Helper()
	p.publish(t, id, messaging.SessionStartedRoutingKey, startedAt, messaging.SessionStartedData{
		SessionID: sessionID,
		StartedAt: startedAt,
	})
}

func (p *publisher) sessionEnded(t *testing.T, id, sessionID, endedAt string) {
	t.Helper()
	// summary[] is required by the schema; item shape is intentionally unpinned
	// (the consumer must not read it) — an empty array is contract-valid.
	p.publish(t, id, messaging.SessionEndedRoutingKey, endedAt, map[string]any{
		"sessionId": sessionID,
		"endedAt":   endedAt,
		"summary":   []any{},
	})
}

func (p *publisher) publish(t *testing.T, id, eventType, occurredAt string, data any) {
	t.Helper()
	env := messaging.Envelope{
		ID:              id,
		Type:            eventType,
		Source:          "timing",
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      occurredAt,
		CorrelationID:   "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		CausationID:     nil,
		Data:            data,
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal %s: %v", eventType, err)
	}
	if err := p.ch.PublishWithContext(context.Background(), timingExchange,
		eventType, false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		}); err != nil {
		t.Fatalf("publish %s: %v", eventType, err)
	}
}

func (p *publisher) close() error {
	_ = p.ch.Close()
	return p.conn.Close()
}
