package persistence

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "timing.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sampleRow(id string) OutboxRow {
	return OutboxRow{
		ID:         id,
		RoutingKey: "lap.recorded",
		Payload:    []byte(`{"id":"` + id + `","type":"lap.recorded"}`),
		CreatedAt:  "2026-06-05T10:00:00.000Z",
	}
}

func countOutbox(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox").Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

// AC1 headline: the domain write and the outbox insert commit in ONE tx. If the
// tx rolls back after the outbox insert, NEITHER row survives.
func TestEnqueueIsAtomicWithDomainWrite(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := NewOutboxStore(db)

	// A stand-in domain table (real lap/session tables arrive in Story 1.5).
	if _, err := db.ExecContext(ctx, `CREATE TABLE laps (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create laps: %v", err)
	}

	// Roll back AFTER writing both the domain row and the outbox row.
	wantErr := context.Canceled
	err := WithinTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO laps (id) VALUES ('lap-1')`); err != nil {
			return err
		}
		if err := store.Enqueue(tx, sampleRow("11111111-1111-7111-8111-111111111111")); err != nil {
			return err
		}
		return wantErr // force rollback
	})
	if err != wantErr {
		t.Fatalf("WithinTx err = %v, want %v", err, wantErr)
	}

	if n := countOutbox(t, db); n != 0 {
		t.Fatalf("outbox rows after rollback = %d, want 0 (not atomic)", n)
	}
	var laps int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM laps").Scan(&laps); err != nil {
		t.Fatalf("count laps: %v", err)
	}
	if laps != 0 {
		t.Fatalf("lap rows after rollback = %d, want 0 (not atomic)", laps)
	}
}

// A committed tx persists the outbox row as pending.
func TestEnqueueCommitPersistsPending(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := NewOutboxStore(db)

	id := "22222222-2222-7222-8222-222222222222"
	if err := WithinTx(ctx, db, func(tx *sql.Tx) error {
		return store.Enqueue(tx, sampleRow(id))
	}); err != nil {
		t.Fatalf("WithinTx: %v", err)
	}

	rows, err := store.FetchPending(ctx, 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("pending rows = %d, want 1", len(rows))
	}
	if rows[0].ID != id || rows[0].Status != "pending" || rows[0].Attempts != 0 {
		t.Fatalf("row = %+v, want id=%s status=pending attempts=0", rows[0], id)
	}
}

// FetchPending returns rows oldest-first so wire order matches production order.
func TestFetchPendingOldestFirst(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := NewOutboxStore(db)

	older := sampleRow("33333333-3333-7333-8333-333333333333")
	older.CreatedAt = "2026-06-05T10:00:00.000Z"
	newer := sampleRow("44444444-4444-7444-8444-444444444444")
	newer.CreatedAt = "2026-06-05T10:00:01.000Z"

	// Insert newer first to prove ordering is by created_at, not insert order.
	for _, r := range []OutboxRow{newer, older} {
		if err := WithinTx(ctx, db, func(tx *sql.Tx) error { return store.Enqueue(tx, r) }); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	rows, err := store.FetchPending(ctx, 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != older.ID {
		t.Fatalf("order = [%s, ...], want oldest %s first", rows[0].ID, older.ID)
	}
}

// MarkSent / MarkQuarantined / RecordFailure drive the row lifecycle.
func TestStatusTransitions(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := NewOutboxStore(db)

	sent := "55555555-5555-7555-8555-555555555555"
	quarantined := "66666666-6666-7666-8666-666666666666"
	retry := "77777777-7777-7777-8777-777777777777"
	for _, id := range []string{sent, quarantined, retry} {
		if err := WithinTx(ctx, db, func(tx *sql.Tx) error { return store.Enqueue(tx, sampleRow(id)) }); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}

	if err := store.MarkSent(ctx, sent, "2026-06-05T10:00:02.000Z"); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	if err := store.MarkQuarantined(ctx, quarantined, "envelope: bad correlationId"); err != nil {
		t.Fatalf("MarkQuarantined: %v", err)
	}
	if err := store.RecordFailure(ctx, retry, "broker unreachable"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	// Only the retry row is still pending (a transient failure keeps it in play).
	pending, err := store.FetchPending(ctx, 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != retry {
		t.Fatalf("pending = %+v, want only %s", pending, retry)
	}
	if pending[0].Attempts != 1 || pending[0].LastError == "" {
		t.Fatalf("retry row attempts=%d lastError=%q, want attempts=1 and a recorded error", pending[0].Attempts, pending[0].LastError)
	}

	assertStatus(t, db, sent, "sent")
	assertStatus(t, db, quarantined, "quarantined")
}

func assertStatus(t *testing.T, db *sql.DB, id, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow("SELECT status FROM outbox WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("status of %s: %v", id, err)
	}
	if got != want {
		t.Fatalf("status of %s = %q, want %q", id, got, want)
	}
}
