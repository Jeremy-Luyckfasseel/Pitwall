package consumer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/persistence"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// pinnedPolicy mirrors the Story-1.9 production DLQ policy (Q&A Round 27).
var pinnedPolicy = domain.DLQPolicy{MaxAttempts: 5, BaseMs: 1000, Multiplier: 2, MaxMs: 60000}

// fakeDelivery is a broker-free messaging.Delivery for unit testing. retryCount
// simulates how many times the DLQ has already redelivered this message.
type fakeDelivery struct {
	body       []byte
	retryCount int
	acked      bool
	nacked     bool
	requeue    bool
}

func (f *fakeDelivery) Body() []byte    { return f.body }
func (f *fakeDelivery) RetryCount() int { return f.retryCount }
func (f *fakeDelivery) Ack() error      { f.acked = true; return nil }
func (f *fakeDelivery) Nack(requeue bool) error {
	f.nacked = true
	f.requeue = requeue
	return nil
}

// parkRec / retryRec capture what the handler routed to the DLQ.
type parkRec struct {
	reason string
	body   []byte
}
type retryRec struct {
	delayMs     int
	nextRetries int
	body        []byte
}

// recorder captures Notify/Park/Retry side-effects and can inject publish errors.
type recorder struct {
	notifyCount int
	parked      []parkRec
	retried     []retryRec
	parkErr     error // simulate a failed park publish
	retryErr    error // simulate a failed retry publish
}

// fakeApplier is an applier whose every method returns errBoom (or applies
// cleanly when err is nil) — used to force the processing-failure path.
type fakeApplier struct{ err error }

func (f *fakeApplier) ApplyLap(_ context.Context, _, _, _, _ string, _ domain.Lap) (bool, bool, error) {
	return f.result()
}
func (f *fakeApplier) ApplySessionStarted(_ context.Context, _, _, _, _, _ string) (bool, bool, error) {
	return f.result()
}
func (f *fakeApplier) ApplySessionEnded(_ context.Context, _, _, _, _, _ string) (bool, bool, error) {
	return f.result()
}
func (f *fakeApplier) result() (bool, bool, error) {
	if f.err != nil {
		return false, false, f.err
	}
	return true, false, nil
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

// buildHandler wires a Handler around the given applier with the real validator,
// the pinned DLQ policy, and Notify/Park/Retry routed into a recorder.
func buildHandler(t *testing.T, store applier) (*Handler, *recorder) {
	t.Helper()
	v, err := messaging.NewValidator(contractDir(t))
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	rec := &recorder{}
	h := &Handler{
		Validate: v.ValidateEnvelopeBytes,
		Store:    store,
		Log:      quietLogger(),
		Notify:   func() { rec.notifyCount++ },
		Now:      func() string { return "2026-06-08T10:00:00.000Z" },
		Policy:   pinnedPolicy,
		Park: func(_ context.Context, body []byte, reason string) error {
			rec.parked = append(rec.parked, parkRec{reason: reason, body: body})
			return rec.parkErr
		},
		Retry: func(_ context.Context, body []byte, delayMs, nextRetries int) error {
			rec.retried = append(rec.retried, retryRec{delayMs: delayMs, nextRetries: nextRetries, body: body})
			return rec.retryErr
		},
	}
	return h, rec
}

// newHandler builds a Handler over a REAL SQLite store (happy-path projection).
func newHandler(t *testing.T) (*Handler, *persistence.Store, *recorder) {
	t.Helper()
	db, err := persistence.Open(context.Background(), filepath.Join(t.TempDir(), "lb.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := persistence.NewStore(db)
	h, rec := buildHandler(t, store)
	return h, store, rec
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

func currentBoard(t *testing.T, store *persistence.Store) *persistence.Board {
	t.Helper()
	b, err := store.CurrentBoard(context.Background())
	if err != nil {
		t.Fatalf("CurrentBoard: %v", err)
	}
	return b
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

// --- happy paths (Story 1.7/1.8 regression) -----------------------------------

// AC1: a valid lap is validated, applied to the read-model, acked, and notifies.
func TestProcess_ValidLap_AppliedAckedNotified(t *testing.T) {
	h, store, rec := newHandler(t)
	d := &fakeDelivery{body: lapEnvelope("11111111-1111-7111-8111-111111111111",
		"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z")}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("valid lap: acked=%v nacked=%v, want acked", d.acked, d.nacked)
	}
	if len(rec.parked) != 0 || len(rec.retried) != 0 {
		t.Errorf("valid lap must not touch the DLQ: parked=%v retried=%v", rec.parked, rec.retried)
	}
	bests := currentBests(t, store)
	if len(bests) != 1 || bests[0].BestLapMs != 42000 {
		t.Errorf("standings = %+v, want one row @42000", bests)
	}
	if rec.notifyCount != 1 {
		t.Errorf("notify called %d times, want 1", rec.notifyCount)
	}
}

// AC1 (M6): a redelivered envelope id is a no-op — acked, not re-applied, no notify.
func TestProcess_DuplicateID_NoOpButAcked(t *testing.T) {
	h, store, rec := newHandler(t)
	body := lapEnvelope("22222222-2222-7222-8222-222222222222",
		"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z")

	h.Process(context.Background(), &fakeDelivery{body: body})
	d2 := &fakeDelivery{body: body}
	h.Process(context.Background(), d2)

	if !d2.acked {
		t.Error("a redelivered message must still be acked (dedupe no-op)")
	}
	if len(currentBests(t, store)) != 1 {
		t.Error("duplicate changed the read-model")
	}
	if rec.notifyCount != 1 {
		t.Errorf("notify called %d times, want 1 (duplicate must not notify)", rec.notifyCount)
	}
}

// Tolerant reader: a valid but unhandled event type (a heartbeat) is ignored.
func TestProcess_UnhandledType_IgnoredAndAcked(t *testing.T) {
	h, store, rec := newHandler(t)
	d := &fakeDelivery{body: fixture(t, contractDir(t), "examples/control/control.heartbeat.v1.example.json")}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("an unhandled type should be acked and ignored: acked=%v nacked=%v", d.acked, d.nacked)
	}
	if len(rec.parked) != 0 || len(rec.retried) != 0 {
		t.Error("an unhandled type must not touch the DLQ")
	}
	if len(currentBests(t, store)) != 0 || rec.notifyCount != 0 {
		t.Error("an unhandled type must not touch the read-model or notify")
	}
}

func TestProcess_SessionStarted_AppliedAckedNotified(t *testing.T) {
	h, store, rec := newHandler(t)
	d := &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/session.started.v1.example.json")}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("session.started: acked=%v nacked=%v, want acked", d.acked, d.nacked)
	}
	b := currentBoard(t, store)
	if b == nil || b.SessionID != "session-2026-05-31-evening-heat-3" || b.Status != persistence.StatusActive {
		t.Errorf("board = %+v, want the example session active", b)
	}
	if rec.notifyCount != 1 {
		t.Errorf("notify = %d, want 1", rec.notifyCount)
	}
}

func TestProcess_SessionEnded_FinishedAndNotified(t *testing.T) {
	h, store, rec := newHandler(t)
	h.Process(context.Background(), &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/session.started.v1.example.json")})
	d := &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/session.ended.v1.example.json")}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("session.ended: acked=%v nacked=%v, want acked", d.acked, d.nacked)
	}
	if b := currentBoard(t, store); b == nil || b.Status != persistence.StatusFinished {
		t.Errorf("board = %+v, want finished", b)
	}
	if rec.notifyCount != 2 {
		t.Errorf("notify = %d, want 2 (start + end)", rec.notifyCount)
	}
}

func TestProcess_DuplicateSessionStarted_NoOpAckedNoNotify(t *testing.T) {
	h, store, rec := newHandler(t)
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
	if rec.notifyCount != 1 {
		t.Errorf("notify = %d, want 1 (duplicate must not notify)", rec.notifyCount)
	}
}

func TestProcess_LapBeforeStart_ImplicitBoard(t *testing.T) {
	h, store, _ := newHandler(t)
	h.Process(context.Background(), &fakeDelivery{body: lapEnvelope(
		"44444444-4444-7444-8444-444444444444", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z")})

	b := currentBoard(t, store)
	if b == nil || b.SessionID != "s1" || b.Status != persistence.StatusImplicit {
		t.Fatalf("board = %+v, want implicit s1", b)
	}
	if len(b.Bests) != 1 {
		t.Errorf("the early lap must land on the implicit board: %+v", b.Bests)
	}
}

func TestProcess_EqualBest_FirstSetRanksHigher(t *testing.T) {
	h, store, _ := newHandler(t)
	h.Process(context.Background(), &fakeDelivery{body: lapEnvelope(
		"aaaaaaaa-0000-7000-8000-000000000001", "aaaaaaaa-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z")})
	h.Process(context.Background(), &fakeDelivery{body: lapEnvelope(
		"bbbbbbbb-0000-7000-8000-000000000002", "bbbbbbbb-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:09.000Z")})

	ranked := domain.Rank(currentBests(t, store))
	if len(ranked) != 2 || ranked[0].MasterID != "aaaaaaaa-3e84-4d11-9aa2-7b6c5e4d3f21" {
		t.Errorf("first-to-set should rank higher; got %+v", ranked)
	}
}

// --- Story 1.9: invalid → park immediately (AC3) ------------------------------

// AC3: an invalid-on-consume message is NOT applied; it is logged and routed
// STRAIGHT to parking (never retried as poison), and acked.
func TestProcess_InvalidPayload_ParkedNotRetried(t *testing.T) {
	h, store, rec := newHandler(t)
	d := &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/lap.recorded.v1.invalid.json")}

	h.Process(context.Background(), d)

	if !d.acked {
		t.Error("an invalid message must be acked after it is parked (we took ownership)")
	}
	if len(rec.retried) != 0 {
		t.Errorf("an invalid message must NOT be retried as poison: %v", rec.retried)
	}
	if len(rec.parked) != 1 || rec.parked[0].reason != "contract-invalid" {
		t.Errorf("invalid message: parked=%v, want one park with reason contract-invalid", rec.parked)
	}
	if len(currentBests(t, store)) != 0 || rec.notifyCount != 0 {
		t.Error("an invalid message must not touch the read-model or notify")
	}
}

func TestProcess_InvalidSessionStarted_Parked(t *testing.T) {
	h, store, rec := newHandler(t)
	d := &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/session.started.v1.invalid.json")}

	h.Process(context.Background(), d)

	if !d.acked || len(rec.retried) != 0 {
		t.Errorf("invalid session.started must be acked and not retried: acked=%v retried=%v", d.acked, rec.retried)
	}
	if len(rec.parked) != 1 || rec.parked[0].reason != "contract-invalid" {
		t.Errorf("parked=%v, want one contract-invalid park", rec.parked)
	}
	if b := currentBoard(t, store); b != nil {
		t.Errorf("an invalid session.started must not touch the read-model: %+v", b)
	}
}

// AC3: an EMPTY sessionId passes /contract (no minLength pinned) but cannot key a
// read-model — it is parked like an invalid message on all three paths.
func TestProcess_EmptySessionID_Parked(t *testing.T) {
	cases := map[string]string{
		"lap.recorded":    `{"id":"55555555-5555-7555-8555-555555555551","type":"lap.recorded","source":"timing","schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-08T10:00:01.000Z","correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{"masterId":"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21","sessionId":"","lapNumber":3,"lapTimeMs":42000,"at":"2026-06-08T10:00:01.000Z","transponderId":null}}`,
		"session.started": `{"id":"55555555-5555-7555-8555-555555555552","type":"session.started","source":"timing","schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-08T10:00:00.000Z","correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{"sessionId":"","startedAt":"2026-06-08T10:00:00.000Z"}}`,
		"session.ended":   `{"id":"55555555-5555-7555-8555-555555555553","type":"session.ended","source":"timing","schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-08T10:20:00.000Z","correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{"sessionId":"","endedAt":"2026-06-08T10:20:00.000Z","summary":[]}}`,
	}
	for typ, body := range cases {
		h, store, rec := newHandler(t)
		d := &fakeDelivery{body: []byte(body)}

		h.Process(context.Background(), d)

		if !d.acked || len(rec.retried) != 0 {
			t.Errorf("%s empty sessionId: acked=%v retried=%v, want acked + not retried", typ, d.acked, rec.retried)
		}
		if len(rec.parked) != 1 || rec.parked[0].reason != "blank-session-id" {
			t.Errorf("%s empty sessionId: parked=%v, want one blank-session-id park", typ, rec.parked)
		}
		if b := currentBoard(t, store); b != nil {
			t.Errorf("%s empty sessionId must not create a board: %+v", typ, b)
		}
		if rec.notifyCount != 0 {
			t.Errorf("%s empty sessionId must not notify", typ)
		}
	}
}

// --- Story 1.9: processing failure → retry then park (AC1, AC2) ----------------

var errBoom = errors.New("transient apply failure")

// AC1: a processing failure below the cap is republished to the retry queue with
// the exponential backoff delay, then the original is acked (ownership taken).
func TestProcess_ApplyError_BelowCap_RetriedAndAcked(t *testing.T) {
	h, rec := buildHandler(t, &fakeApplier{err: errBoom})
	d := &fakeDelivery{body: lapEnvelope("66666666-6666-7666-8666-666666666661",
		"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z"), retryCount: 0}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("retry path: acked=%v nacked=%v, want acked (republished to retry)", d.acked, d.nacked)
	}
	if len(rec.parked) != 0 {
		t.Errorf("below the cap must NOT park: %v", rec.parked)
	}
	if len(rec.retried) != 1 || rec.retried[0].delayMs != 1000 || rec.retried[0].nextRetries != 1 {
		t.Errorf("retried = %v, want one retry @1000ms next=1", rec.retried)
	}
	if rec.notifyCount != 0 {
		t.Error("a failed apply must not notify")
	}
}

// AC1: the backoff escalates with the redelivery count (hop 4 → 8 s).
func TestProcess_ApplyError_EscalatesBackoff(t *testing.T) {
	h, rec := buildHandler(t, &fakeApplier{err: errBoom})
	d := &fakeDelivery{body: lapEnvelope("66666666-6666-7666-8666-666666666662",
		"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z"), retryCount: 3}

	h.Process(context.Background(), d)

	if len(rec.retried) != 1 || rec.retried[0].delayMs != 8000 || rec.retried[0].nextRetries != 4 {
		t.Errorf("retried = %v, want one retry @8000ms next=4", rec.retried)
	}
}

// AC2: the attempt that reaches the cap is parked (+ alert) and acked, never
// requeued — a poison message is terminated, not looped forever.
func TestProcess_ApplyError_AtCap_ParkedAndAcked(t *testing.T) {
	h, rec := buildHandler(t, &fakeApplier{err: errBoom})
	d := &fakeDelivery{body: lapEnvelope("66666666-6666-7666-8666-666666666663",
		"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z"), retryCount: 4}

	h.Process(context.Background(), d)

	if !d.acked || d.nacked {
		t.Errorf("park-at-cap: acked=%v nacked=%v, want acked", d.acked, d.nacked)
	}
	if len(rec.retried) != 0 {
		t.Errorf("at the cap there must be NO further retry: %v", rec.retried)
	}
	if len(rec.parked) != 1 || rec.parked[0].reason != "retries-exhausted" {
		t.Errorf("parked = %v, want one retries-exhausted park", rec.parked)
	}
}

// AC1/NFR6: if the retry republish itself fails (broker hiccup), the original is
// NOT acked — it is requeued so the message is never lost.
func TestProcess_RetryPublishFails_NackRequeue(t *testing.T) {
	h, rec := buildHandler(t, &fakeApplier{err: errBoom})
	rec.retryErr = errors.New("broker down")
	d := &fakeDelivery{body: lapEnvelope("66666666-6666-7666-8666-666666666664",
		"1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", 42000, "2026-06-08T10:00:01.000Z"), retryCount: 0}

	h.Process(context.Background(), d)

	if d.acked {
		t.Error("a failed retry publish must NOT ack (would lose the message)")
	}
	if !d.nacked || !d.requeue {
		t.Errorf("retry-publish failure: nacked=%v requeue=%v, want nacked requeue=true", d.nacked, d.requeue)
	}
}

// If a park publish fails, the original is likewise requeued, never acked.
func TestProcess_ParkPublishFails_NackRequeue(t *testing.T) {
	h, _, rec := newHandler(t)
	rec.parkErr = errors.New("broker down")
	d := &fakeDelivery{body: fixture(t, contractDir(t), "examples/timing/lap.recorded.v1.invalid.json")}

	h.Process(context.Background(), d)

	if d.acked {
		t.Error("a failed park publish must NOT ack")
	}
	if !d.nacked || !d.requeue {
		t.Errorf("park-publish failure: nacked=%v requeue=%v, want nacked requeue=true", d.nacked, d.requeue)
	}
}
