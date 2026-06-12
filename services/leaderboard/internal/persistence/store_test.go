package persistence

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "lb.db")
	db, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func lap(master string, ms int64, at string, seq int64) domain.Lap {
	return domain.Lap{MasterID: master, LapTimeMs: ms, At: at, Seq: seq}
}

// board is a test helper: CurrentBoard must never error NOR be nil in these
// scenarios (fatal on nil so failing tests report instead of nil-panicking;
// the legitimate-nil case is covered by TestCurrentBoard_Empty_NilBoard).
func board(t *testing.T, s *Store) *Board {
	t.Helper()
	b, err := s.CurrentBoard(context.Background())
	if err != nil {
		t.Fatalf("CurrentBoard: %v", err)
	}
	if b == nil {
		t.Fatal("CurrentBoard returned nil; a session was expected")
	}
	return b
}

// --- lap application on the session-keyed projection (AC1 regression + AC3) ---

// A first lap is applied and projected under its session.
func TestApplyLap_FirstLap_IsAppliedAndProjected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	applied, dup, err := s.ApplyLap(ctx, "id-1", "lap.recorded", "2026-06-08T10:00:01.000Z",
		"s1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))
	if err != nil {
		t.Fatalf("ApplyLap: %v", err)
	}
	if !applied || dup {
		t.Fatalf("first lap: applied=%v dup=%v, want applied=true dup=false", applied, dup)
	}
	b := board(t, s)
	if b == nil || b.SessionID != "s1" {
		t.Fatalf("board = %+v, want session s1", b)
	}
	if len(b.Bests) != 1 || b.Bests[0].MasterID != "a" || b.Bests[0].BestLapMs != 42000 {
		t.Fatalf("bests = %+v, want one row a@42000", b.Bests)
	}
}

// M6: a REDELIVERED envelope id is a no-op — not re-applied, read-model unchanged.
func TestApplyLap_DuplicateEnvelopeID_IsNoOp(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	l := lap("a", 42000, "2026-06-08T10:00:01.000Z", 1)

	if _, _, err := s.ApplyLap(ctx, "id-1", "lap.recorded", l.At, "s1", l); err != nil {
		t.Fatalf("first ApplyLap: %v", err)
	}
	applied, dup, err := s.ApplyLap(ctx, "id-1", "lap.recorded", l.At, "s1", l) // same id again
	if err != nil {
		t.Fatalf("second ApplyLap: %v", err)
	}
	if applied || !dup {
		t.Errorf("redelivered id: applied=%v dup=%v, want applied=false dup=true", applied, dup)
	}
	b := board(t, s)
	if len(b.Bests) != 1 || b.Bests[0].BestLapMs != 42000 {
		t.Errorf("standings changed after a duplicate: %+v", b.Bests)
	}
}

// A slower later lap does NOT worsen the best; a faster one improves it; an
// equal one keeps the FIRST `at` (the FR44 tie-break key) — all within a session.
func TestApplyLap_BestRuleWithinSession(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplyLap(ctx, "id-1", "lap.recorded", "t1", "s1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))

	if applied, _, _ := s.ApplyLap(ctx, "id-2", "lap.recorded", "t2", "s1", lap("a", 43000, "2026-06-08T10:00:02.000Z", 2)); applied {
		t.Error("a slower lap should report applied=false (no improvement)")
	}
	if applied, _, _ := s.ApplyLap(ctx, "id-3", "lap.recorded", "t3", "s1", lap("a", 42000, "2026-06-08T10:00:09.000Z", 3)); applied {
		t.Error("an equal lap must not be re-applied (first-to-set wins)")
	}
	b := board(t, s)
	if b.Bests[0].BestLapMs != 42000 || b.Bests[0].BestLapAt != "2026-06-08T10:00:01.000Z" {
		t.Errorf("best moved on slower/equal laps: %+v", b.Bests[0])
	}

	if applied, _, _ := s.ApplyLap(ctx, "id-4", "lap.recorded", "t4", "s1", lap("a", 41000, "2026-06-08T10:00:15.000Z", 4)); !applied {
		t.Error("a faster lap must be applied")
	}
	b = board(t, s)
	if b.Bests[0].BestLapMs != 41000 || b.Bests[0].BestLapAt != "2026-06-08T10:00:15.000Z" {
		t.Errorf("best not improved: %+v", b.Bests[0])
	}
}

// AC3 (the re-key headline): the same driver in two sessions holds two
// INDEPENDENT bests — the new session starts from scratch (FR43), the old
// session's row survives untouched (non-destructive reset).
func TestApplyLap_SameDriverTwoSessions_IndependentBests(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplySessionStarted(ctx, "ev-s1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	_, _, _ = s.ApplyLap(ctx, "id-1", "lap.recorded", "t1", "s1", lap("a", 41000, "2026-06-08T10:00:01.000Z", 1))
	_, _, _ = s.ApplySessionStarted(ctx, "ev-s2", "session.started", "t2", "s2", "2026-06-08T11:00:00.000Z")
	_, _, _ = s.ApplyLap(ctx, "id-2", "lap.recorded", "t3", "s2", lap("a", 45000, "2026-06-08T11:00:01.000Z", 2))

	b := board(t, s)
	if b.SessionID != "s2" {
		t.Fatalf("current board = %q, want the newest session s2", b.SessionID)
	}
	if len(b.Bests) != 1 || b.Bests[0].BestLapMs != 45000 {
		t.Errorf("s2 board must hold only the s2 best (45000), got %+v", b.Bests)
	}
}

// --- session lifecycle gating (AC2 + AC3) ---

// session.started on an unknown session creates it ACTIVE and makes it current.
func TestApplySessionStarted_NewSession_ActiveAndCurrent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	applied, dup, err := s.ApplySessionStarted(ctx, "ev-1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	if err != nil {
		t.Fatalf("ApplySessionStarted: %v", err)
	}
	if !applied || dup {
		t.Fatalf("new session: applied=%v dup=%v, want applied=true dup=false", applied, dup)
	}
	b := board(t, s)
	if b == nil || b.SessionID != "s1" || b.Status != StatusActive {
		t.Errorf("board = %+v, want s1 active", b)
	}
	if len(b.Bests) != 0 {
		t.Errorf("a fresh session must have an empty board, got %+v", b.Bests)
	}
}

// session.ended marks the session finished; the final standings stay readable.
func TestApplySessionEnded_KnownSession_Finished(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplySessionStarted(ctx, "ev-1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	_, _, _ = s.ApplyLap(ctx, "id-1", "lap.recorded", "t1", "s1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))

	applied, _, err := s.ApplySessionEnded(ctx, "ev-2", "session.ended", "t2", "s1", "2026-06-08T10:20:00.000Z")
	if err != nil {
		t.Fatalf("ApplySessionEnded: %v", err)
	}
	if !applied {
		t.Error("ending a live session must report applied=true (board change)")
	}
	b := board(t, s)
	if b.Status != StatusFinished {
		t.Errorf("status = %q, want finished", b.Status)
	}
	if len(b.Bests) != 1 {
		t.Errorf("final standings must remain on the finished board, got %+v", b.Bests)
	}
}

// AC3: a lap BEFORE its session.started creates an IMPLICIT board keyed on the
// lap's sessionId; the late session.started then reconciles (implicit → active)
// without touching the laps already on it.
func TestOutOfOrder_LapBeforeStart_ImplicitBoardThenReconcile(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, _, _ = s.ApplyLap(ctx, "id-1", "lap.recorded", "t1", "s1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))
	b := board(t, s)
	if b == nil || b.SessionID != "s1" || b.Status != StatusImplicit {
		t.Fatalf("board after early lap = %+v, want implicit s1", b)
	}
	if len(b.Bests) != 1 {
		t.Fatalf("the early lap must land on the implicit board, got %+v", b.Bests)
	}

	applied, _, err := s.ApplySessionStarted(ctx, "ev-1", "session.started", "t2", "s1", "2026-06-08T10:00:00.000Z")
	if err != nil {
		t.Fatalf("late ApplySessionStarted: %v", err)
	}
	if !applied {
		t.Error("reconciling implicit → active must report applied=true")
	}
	b = board(t, s)
	if b.Status != StatusActive {
		t.Errorf("status = %q, want active after reconcile", b.Status)
	}
	if len(b.Bests) != 1 || b.Bests[0].BestLapMs != 42000 {
		t.Errorf("reconcile must not touch the laps: %+v", b.Bests)
	}
}

// AC3: a REPLAYED session.started under a fresh envelope id is an idempotent
// upsert — it never wipes the session's standings, never re-bumps its epoch
// (the live board stays current), and reports applied=false (no notify churn).
func TestReplayedStart_SameSession_NoWipeNoReorder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplySessionStarted(ctx, "ev-1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	_, _, _ = s.ApplyLap(ctx, "id-1", "lap.recorded", "t1", "s1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 1))

	applied, dup, err := s.ApplySessionStarted(ctx, "ev-9", "session.started", "t2", "s1", "2026-06-08T10:00:00.000Z")
	if err != nil {
		t.Fatalf("replayed ApplySessionStarted: %v", err)
	}
	if dup {
		t.Error("a fresh envelope id is not an inbox duplicate")
	}
	if applied {
		t.Error("an identical replayed start changes nothing: applied=false expected")
	}
	b := board(t, s)
	if b.SessionID != "s1" || b.Status != StatusActive {
		t.Errorf("board = %+v, want s1 still active", b)
	}
	if len(b.Bests) != 1 {
		t.Errorf("a replayed start must NOT wipe the standings: %+v", b.Bests)
	}
}

// AC3: a replayed start for an EARLIER, finished session neither reopens it nor
// steals currency from the live board (status forward-only; epoch unmoved).
func TestReplayedStart_OldFinishedSession_DoesNotDisturbLiveBoard(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplySessionStarted(ctx, "ev-1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	_, _, _ = s.ApplySessionEnded(ctx, "ev-2", "session.ended", "t1", "s1", "2026-06-08T10:20:00.000Z")
	_, _, _ = s.ApplySessionStarted(ctx, "ev-3", "session.started", "t2", "s2", "2026-06-08T11:00:00.000Z")
	_, _, _ = s.ApplyLap(ctx, "id-1", "lap.recorded", "t3", "s2", lap("a", 42000, "2026-06-08T11:00:01.000Z", 1))

	applied, _, err := s.ApplySessionStarted(ctx, "ev-9", "session.started", "t4", "s1", "2026-06-08T10:00:00.000Z")
	if err != nil {
		t.Fatalf("replayed old start: %v", err)
	}
	if applied {
		t.Error("a replayed start on a finished session changes nothing visible: applied=false expected")
	}
	b := board(t, s)
	if b.SessionID != "s2" || b.Status != StatusActive {
		t.Errorf("live board disturbed by an old session's replayed start: %+v", b)
	}
	if len(b.Bests) != 1 {
		t.Errorf("live standings disturbed: %+v", b.Bests)
	}
}

// AC3: session.ended for an UNKNOWN session is never dropped — it implicit-creates
// the session directly as finished (reconcile-not-corrupt).
func TestApplySessionEnded_UnknownSession_CreatedFinished(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	applied, _, err := s.ApplySessionEnded(ctx, "ev-1", "session.ended", "t0", "s1", "2026-06-08T10:20:00.000Z")
	if err != nil {
		t.Fatalf("ApplySessionEnded: %v", err)
	}
	if !applied {
		t.Error("an end for an unknown session must still be recorded (applied=true)")
	}
	b := board(t, s)
	if b == nil || b.SessionID != "s1" || b.Status != StatusFinished {
		t.Errorf("board = %+v, want s1 finished", b)
	}
}

// Forward-only: a (late or replayed) start arriving AFTER the end never reopens
// the session — finished is terminal (reconcile, don't flip back).
func TestStartAfterEnd_DoesNotReopen(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplySessionStarted(ctx, "ev-1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	_, _, _ = s.ApplySessionEnded(ctx, "ev-2", "session.ended", "t1", "s1", "2026-06-08T10:20:00.000Z")

	if _, _, err := s.ApplySessionStarted(ctx, "ev-3", "session.started", "t2", "s1", "2026-06-08T10:00:00.000Z"); err != nil {
		t.Fatalf("start-after-end: %v", err)
	}
	if b := board(t, s); b.Status != StatusFinished {
		t.Errorf("status = %q, want finished (forward-only)", b.Status)
	}
}

// A late lap for a finished session still upserts THAT session's standings
// (never dropped) without disturbing which session is current.
func TestLateLap_FinishedSession_UpsertsWithoutStealingCurrency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplySessionStarted(ctx, "ev-1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	_, _, _ = s.ApplySessionEnded(ctx, "ev-2", "session.ended", "t1", "s1", "2026-06-08T10:20:00.000Z")
	_, _, _ = s.ApplySessionStarted(ctx, "ev-3", "session.started", "t2", "s2", "2026-06-08T11:00:00.000Z")

	applied, _, err := s.ApplyLap(ctx, "id-9", "lap.recorded", "t3", "s1", lap("a", 42000, "2026-06-08T10:19:59.000Z", 1))
	if err != nil {
		t.Fatalf("late lap: %v", err)
	}
	if !applied {
		t.Error("a late lap initializing a best must report applied=true")
	}
	if b := board(t, s); b.SessionID != "s2" {
		t.Errorf("a late lap for an old session must not steal currency: board = %+v", b)
	}
}

// Epoch is order-of-FIRST-SIGHT: whichever sessionId appears last (by any event)
// is the current board, regardless of timestamps inside the events.
func TestEpoch_OrderOfFirstSight(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplySessionStarted(ctx, "ev-1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	// s2 first seen via an early lap (no session.started yet).
	_, _, _ = s.ApplyLap(ctx, "id-1", "lap.recorded", "t1", "s2", lap("a", 42000, "2026-06-08T09:00:00.000Z", 1))

	b := board(t, s)
	if b.SessionID != "s2" || b.Status != StatusImplicit {
		t.Errorf("board = %+v, want s2 implicit (last first-seen wins)", b)
	}
}

// CurrentBoard on a fresh database: no session yet → nil board, no error.
func TestCurrentBoard_Empty_NilBoard(t *testing.T) {
	s := openTestStore(t)
	b, err := s.CurrentBoard(context.Background())
	if err != nil {
		t.Fatalf("CurrentBoard: %v", err)
	}
	if b != nil {
		t.Errorf("fresh DB: board = %+v, want nil", b)
	}
}

// The board's bests come back pre-ordered (best asc, earliest-set, ingest seq) —
// same order contract AllBests had in 1.7.
func TestCurrentBoard_BestsOrdered(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplySessionStarted(ctx, "ev-1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	_, _, _ = s.ApplyLap(ctx, "id-1", "lap.recorded", "t1", "s1", lap("b", 42000, "2026-06-08T10:00:05.000Z", 1))
	_, _, _ = s.ApplyLap(ctx, "id-2", "lap.recorded", "t2", "s1", lap("a", 41000, "2026-06-08T10:00:09.000Z", 2))
	_, _, _ = s.ApplyLap(ctx, "id-3", "lap.recorded", "t3", "s1", lap("c", 42000, "2026-06-08T10:00:01.000Z", 3))

	b := board(t, s)
	if len(b.Bests) != 3 {
		t.Fatalf("want 3 rows, got %d", len(b.Bests))
	}
	order := []string{b.Bests[0].MasterID, b.Bests[1].MasterID, b.Bests[2].MasterID}
	// a fastest; c and b share 42000 but c's best was set earlier (10:00:01 < 10:00:05).
	if order[0] != "a" || order[1] != "c" || order[2] != "b" {
		t.Errorf("order = %v, want [a c b]", order)
	}
}

// MaxSeq returns the highest stored ingest sequence across ALL sessions (0 when
// empty) so the consumer's tie-break counter stays monotonic across restarts.
func TestMaxSeq(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if got, err := s.MaxSeq(ctx); err != nil || got != 0 {
		t.Fatalf("MaxSeq on empty = %d, %v; want 0, nil", got, err)
	}
	_, _, _ = s.ApplyLap(ctx, "id-1", "lap.recorded", "t1", "s1", lap("a", 42000, "2026-06-08T10:00:01.000Z", 5))
	_, _, _ = s.ApplyLap(ctx, "id-2", "lap.recorded", "t2", "s2", lap("b", 41000, "2026-06-08T10:00:02.000Z", 9))
	if got, err := s.MaxSeq(ctx); err != nil || got != 9 {
		t.Errorf("MaxSeq = %d, %v; want 9, nil", got, err)
	}
}

// The atomic-consume contract every Apply* relies on: an error inside the
// transaction rolls EVERYTHING back — a crash mid-apply can neither
// apply-without-marking nor mark-without-applying (NFR6).
func TestWithinTx_RollsBackEverythingOnError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lb.db")
	db, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	boom := errors.New("boom")
	err = WithinTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO inbox (id, type, processed_at) VALUES ('ev-x', 'lap.recorded', 't1')`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (session_id, epoch, status) VALUES ('s1', 1, 'active')`); err != nil {
			return err
		}
		return boom // simulated failure AFTER both writes
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithinTx error = %v, want the injected failure", err)
	}

	for _, table := range []string{"inbox", "sessions", "standings"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows after a rolled-back tx, want 0", table, n)
		}
	}
}

// Duplicate envelope ids on session events are inbox no-ops too (M6 extends to
// the lifecycle events, not just laps).
func TestApplySessionStarted_DuplicateEnvelopeID_IsNoOp(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.ApplySessionStarted(ctx, "ev-1", "session.started", "t0", "s1", "2026-06-08T10:00:00.000Z")
	_, _, _ = s.ApplySessionEnded(ctx, "ev-2", "session.ended", "t1", "s1", "2026-06-08T10:20:00.000Z")

	applied, dup, err := s.ApplySessionStarted(ctx, "ev-1", "session.started", "t2", "s1", "2026-06-08T10:00:00.000Z")
	if err != nil {
		t.Fatalf("duplicate ApplySessionStarted: %v", err)
	}
	if applied || !dup {
		t.Errorf("redelivered start: applied=%v dup=%v, want applied=false dup=true", applied, dup)
	}
	if b := board(t, s); b.Status != StatusFinished {
		t.Errorf("a redelivered start must not flip status: %+v", b)
	}
}
