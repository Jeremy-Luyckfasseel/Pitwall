package consumer

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/persistence"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// fakeDelivery is a broker-free messaging.Delivery for unit testing.
type fakeDelivery struct {
	body    []byte
	acked   bool
	nacked  bool
	requeue bool
}

func (f *fakeDelivery) Body() []byte { return f.body }
func (f *fakeDelivery) Ack() error   { f.acked = true; return nil }
func (f *fakeDelivery) Nack(requeue bool) error {
	f.nacked = true
	f.requeue = requeue
	return nil
}

// currentBests reads the current board's bests ([] when no session exists yet).
func currentBests(t *testing.T, store *persistence.Store) []domain.DriverBest {
	t.Helper()
	b, err := store.CurrentBoard(context.Background())
	if err != nil {
		t.Fatalf("CurrentBoard: %v", err)
	}
	if b == nil {
		return nil
	}
	return b.Bests
}

func contractDir(t *testing.T) string {
	t.Helper()
	dir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("resolve /contract: %v", err)
	}
	return dir
}

func fixture(t *testing.T, dir, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return b
}

func newHandler(t *testing.T) (*Handler, *persistence.Store, *int) {
	t.Helper()
	dir := contractDir(t)
	v, err := messaging.NewValidator(dir)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db, err := persistence.Open(context.Background(), filepath.Join(t.TempDir(), "lb.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := persistence.NewStore(db)
	var notifyCount int
	var mu sync.Mutex
	h := &Handler{
		Validate: v.ValidateEnvelopeBytes,
		Store:    store,
		Log:      quietLogger(),
		Notify: func() {
			mu.Lock()
			notifyCount++
			mu.Unlock()
		},
		Now: func() string { return "2026-06-08T10:00:00.000Z" },
	}
	return h, store, &notifyCount
}

// A synthetic, contract-valid lap.recorded envelope with controllable fields.
func lapEnvelope(id, master string, lapMs int64, at string) []byte {
	return []byte(`{"id":"` + id + `","type":"lap.recorded","source":"timing","schemaVersion":1,"envelopeVersion":1,"occurredAt":"` + at + `","correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{"masterId":"` + master + `","sessionId":"s1","lapNumber":3,"lapTimeMs":` + itoa(lapMs) + `,"at":"` + at + `","transponderId":null}}`)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// AC1: a valid lap is validated, applied to the read-model, acked, and notifies.
func TestProcess_ValidLap_AppliedAckedNotified(t *testing.T) {
	h, store, notify := newHandler(t)
	d := &fakeDelivery{body: lapEnvelope("11111111-1111-7111-8111-111111111111",
		"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z")}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("valid lap: acked=%v nacked=%v, want acked", d.acked, d.nacked)
	}
	bests := currentBests(t, store)
	if len(bests) != 1 || bests[0].BestLapMs != 42000 {
		t.Errorf("standings = %+v, want one row @42000", bests)
	}
	if *notify != 1 {
		t.Errorf("notify called %d times, want 1", *notify)
	}
}

// AC1 (M6): a redelivered envelope id is a no-op — acked, not re-applied, no notify.
func TestProcess_DuplicateID_NoOpButAcked(t *testing.T) {
	h, store, notify := newHandler(t)
	body := lapEnvelope("22222222-2222-7222-8222-222222222222",
		"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z")

	h.Process(context.Background(), &fakeDelivery{body: body})
	d2 := &fakeDelivery{body: body}
	h.Process(context.Background(), d2)

	if !d2.acked {
		t.Error("a redelivered message must still be acked (dedupe no-op)")
	}
	bests := currentBests(t, store)
	if len(bests) != 1 {
		t.Errorf("duplicate changed the read-model: %+v", bests)
	}
	if *notify != 1 {
		t.Errorf("notify called %d times, want 1 (duplicate must not notify)", *notify)
	}
}

// AC1: an invalid-on-consume message is NOT applied; it is logged + nacked without
// requeue (no poison loop). Story 1.9 adds the DLX so this becomes a dead-letter.
func TestProcess_InvalidPayload_NotAppliedNackedNoRequeue(t *testing.T) {
	h, store, notify := newHandler(t)
	bad := fixture(t, contractDir(t), "examples/timing/lap.recorded.v1.invalid.json")
	d := &fakeDelivery{body: bad}

	h.Process(context.Background(), d)

	if d.acked {
		t.Error("an invalid message must NOT be acked")
	}
	if !d.nacked || d.requeue {
		t.Errorf("invalid message: nacked=%v requeue=%v, want nacked with requeue=false", d.nacked, d.requeue)
	}
	bests := currentBests(t, store)
	if len(bests) != 0 {
		t.Errorf("invalid message was applied to the read-model: %+v", bests)
	}
	if *notify != 0 {
		t.Error("invalid message must not notify")
	}
}

// Tolerant reader: a valid but unhandled event type (a heartbeat) is ignored —
// acked, not applied. (session.* is HANDLED since Story 1.8; the guard branch
// still protects against future event types.)
func TestProcess_UnhandledType_IgnoredAndAcked(t *testing.T) {
	h, store, notify := newHandler(t)
	other := fixture(t, contractDir(t), "examples/control/control.heartbeat.v1.example.json")
	d := &fakeDelivery{body: other}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("an unhandled type should be acked and ignored: acked=%v nacked=%v", d.acked, d.nacked)
	}
	bests := currentBests(t, store)
	if len(bests) != 0 {
		t.Errorf("an unhandled type must not touch the read-model: %+v", bests)
	}
	if *notify != 0 {
		t.Error("an unhandled type must not notify")
	}
}

// --- Story 1.8: session lifecycle routing (AC2 + AC3) ---

// currentBoard reads the full current board state (nil when no session yet).
func currentBoard(t *testing.T, store *persistence.Store) *persistence.Board {
	t.Helper()
	b, err := store.CurrentBoard(context.Background())
	if err != nil {
		t.Fatalf("CurrentBoard: %v", err)
	}
	return b
}

// AC2: a session.started is validated, applied (board active), acked, and
// NOTIFIES — a session flip is a board change even with no lap moved.
func TestProcess_SessionStarted_AppliedAckedNotified(t *testing.T) {
	h, store, notify := newHandler(t)
	d := &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/session.started.v1.example.json")}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("session.started: acked=%v nacked=%v, want acked", d.acked, d.nacked)
	}
	b := currentBoard(t, store)
	if b == nil || b.SessionID != "session-2026-05-31-evening-heat-3" || b.Status != persistence.StatusActive {
		t.Errorf("board = %+v, want the example session active", b)
	}
	if *notify != 1 {
		t.Errorf("notify called %d times, want 1 (a session flip changes the board)", *notify)
	}
}

// AC2: a session.ended marks the session finished and notifies.
func TestProcess_SessionEnded_FinishedAndNotified(t *testing.T) {
	h, store, notify := newHandler(t)
	h.Process(context.Background(), &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/session.started.v1.example.json")})
	d := &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/session.ended.v1.example.json")}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("session.ended: acked=%v nacked=%v, want acked", d.acked, d.nacked)
	}
	if b := currentBoard(t, store); b == nil || b.Status != persistence.StatusFinished {
		t.Errorf("board = %+v, want finished", b)
	}
	if *notify != 2 {
		t.Errorf("notify called %d times, want 2 (start + end)", *notify)
	}
}

// AC3 (M6): a redelivered session.started envelope id is a no-op — acked, not
// re-applied, and crucially does NOT notify (no SSE churn on duplicates).
func TestProcess_DuplicateSessionStarted_NoOpAckedNoNotify(t *testing.T) {
	h, store, notify := newHandler(t)
	body := fixture(t, contractDir(t), "examples/timing/session.started.v1.example.json")
	h.Process(context.Background(), &fakeDelivery{body: body})
	d2 := &fakeDelivery{body: body}

	h.Process(context.Background(), d2)

	if !d2.acked {
		t.Error("a redelivered session.started must still be acked (dedupe no-op)")
	}
	if b := currentBoard(t, store); b == nil || b.Status != persistence.StatusActive {
		t.Errorf("board = %+v, want still active", b)
	}
	if *notify != 1 {
		t.Errorf("notify called %d times, want 1 (duplicate must not notify)", *notify)
	}
}

// AC3: an invalid session.started (committed known-bad fixture) is never applied —
// logged + nacked without requeue, exactly like an invalid lap.
func TestProcess_InvalidSessionStarted_NotAppliedNackedNoRequeue(t *testing.T) {
	h, store, notify := newHandler(t)
	d := &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/session.started.v1.invalid.json")}

	h.Process(context.Background(), d)

	if d.acked {
		t.Error("an invalid session.started must NOT be acked")
	}
	if !d.nacked || d.requeue {
		t.Errorf("invalid session.started: nacked=%v requeue=%v, want nacked with requeue=false", d.nacked, d.requeue)
	}
	if b := currentBoard(t, store); b != nil {
		t.Errorf("an invalid session.started must not touch the read-model: %+v", b)
	}
	if *notify != 0 {
		t.Error("an invalid session.started must not notify")
	}
}

// Review fix: an EMPTY sessionId passes /contract validation (no minLength is
// pinned on the wire) but cannot key a read-model — it is rejected like an
// invalid message on all three paths, never implicit-creating a blank board.
func TestProcess_EmptySessionID_RejectedNotApplied(t *testing.T) {
	cases := map[string]string{
		"lap.recorded":    `{"id":"55555555-5555-7555-8555-555555555551","type":"lap.recorded","source":"timing","schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-08T10:00:01.000Z","correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{"masterId":"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21","sessionId":"","lapNumber":3,"lapTimeMs":42000,"at":"2026-06-08T10:00:01.000Z","transponderId":null}}`,
		"session.started": `{"id":"55555555-5555-7555-8555-555555555552","type":"session.started","source":"timing","schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-08T10:00:00.000Z","correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{"sessionId":"","startedAt":"2026-06-08T10:00:00.000Z"}}`,
		"session.ended":   `{"id":"55555555-5555-7555-8555-555555555553","type":"session.ended","source":"timing","schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-08T10:20:00.000Z","correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{"sessionId":"","endedAt":"2026-06-08T10:20:00.000Z","summary":[]}}`,
	}
	for typ, body := range cases {
		h, store, notify := newHandler(t)
		d := &fakeDelivery{body: []byte(body)}

		h.Process(context.Background(), d)

		if d.acked {
			t.Errorf("%s with empty sessionId must NOT be acked", typ)
		}
		if !d.nacked || d.requeue {
			t.Errorf("%s with empty sessionId: nacked=%v requeue=%v, want nacked no-requeue", typ, d.nacked, d.requeue)
		}
		if b := currentBoard(t, store); b != nil {
			t.Errorf("%s with empty sessionId must not create a board: %+v", typ, b)
		}
		if *notify != 0 {
			t.Errorf("%s with empty sessionId must not notify", typ)
		}
	}
}

// AC3: a lap arriving BEFORE its session.started routes its sessionId into the
// store and starts the implicit board (the consumer threads sessionId through).
func TestProcess_LapBeforeStart_ImplicitBoard(t *testing.T) {
	h, store, _ := newHandler(t)
	h.Process(context.Background(), &fakeDelivery{body: lapEnvelope(
		"44444444-4444-7444-8444-444444444444", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z")})

	b := currentBoard(t, store)
	if b == nil || b.SessionID != "s1" || b.Status != persistence.StatusImplicit {
		t.Fatalf("board = %+v, want implicit s1 (lap's sessionId threaded through)", b)
	}
	if len(b.Bests) != 1 {
		t.Errorf("the early lap must land on the implicit board: %+v", b.Bests)
	}
}

// AC2 seed: two drivers with an equal best, the later-arriving one ranks lower —
// proven here end-to-end through the handler (validate→apply) + domain.Rank.
func TestProcess_EqualBest_FirstSetRanksHigher(t *testing.T) {
	h, store, _ := newHandler(t)
	// A sets 42000 first (earlier `at`), then B matches 42000 later.
	h.Process(context.Background(), &fakeDelivery{body: lapEnvelope(
		"aaaaaaaa-0000-7000-8000-000000000001", "aaaaaaaa-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z")})
	h.Process(context.Background(), &fakeDelivery{body: lapEnvelope(
		"bbbbbbbb-0000-7000-8000-000000000002", "bbbbbbbb-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:09.000Z")})

	bests := currentBests(t, store)
	ranked := domain.Rank(bests)
	if len(ranked) != 2 || ranked[0].MasterID != "aaaaaaaa-3e84-4d11-9aa2-7b6c5e4d3f21" {
		t.Errorf("first-to-set should rank higher; got %+v", ranked)
	}
}
