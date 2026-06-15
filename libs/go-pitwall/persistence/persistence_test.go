package persistence

import (
	"context"
	"database/sql"
	"embed"
	"path/filepath"
	"testing"
)

//go:embed testdata/migrations/*.sql
var testMigrationsFS embed.FS

// openMigrated opens a fresh temp DB and applies the blueprint test schema.
func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(context.Background(), db, testMigrationsFS, "testdata/migrations"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Open applies the standard pragmas (WAL + foreign_keys on) the mechanics rely on.
func TestOpenSetsPragmas(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}
}

// Migrate creates the schema and is idempotent (re-running Up is a no-op).
func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if !tableExists(t, db, "outbox") || !tableExists(t, db, "inbox") {
		t.Fatalf("expected outbox+inbox tables after migrate")
	}
	// Re-running must not error or duplicate.
	if err := Migrate(ctx, db, testMigrationsFS, "testdata/migrations"); err != nil {
		t.Fatalf("second Migrate (idempotence): %v", err)
	}
}

func sampleRow(id string) OutboxRow {
	return OutboxRow{
		ID:         id,
		RoutingKey: "some.event",
		Payload:    []byte(`{"id":"` + id + `","type":"some.event"}`),
		CreatedAt:  "2026-06-05T10:00:00.000Z",
	}
}

// AC headline: the domain write and the outbox insert commit in ONE tx. A rollback
// after the outbox insert leaves NEITHER row.
func TestEnqueueIsAtomicWithDomainWrite(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	store := NewOutboxStore(db)
	if _, err := db.ExecContext(ctx, `CREATE TABLE thing (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create thing: %v", err)
	}
	wantErr := context.Canceled
	err := WithinTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO thing (id) VALUES ('t-1')`); err != nil {
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
}

// FetchPending returns rows oldest-first and the status transitions drive the
// lifecycle (sent / quarantined terminal; record-failure stays pending).
func TestOutboxLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	store := NewOutboxStore(db)

	older := sampleRow("33333333-3333-7333-8333-333333333333")
	older.CreatedAt = "2026-06-05T10:00:00.000Z"
	newer := sampleRow("44444444-4444-7444-8444-444444444444")
	newer.CreatedAt = "2026-06-05T10:00:01.000Z"
	for _, r := range []OutboxRow{newer, older} { // insert newer first to prove ordering
		if err := WithinTx(ctx, db, func(tx *sql.Tx) error { return store.Enqueue(tx, r) }); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	pending, err := store.FetchPending(ctx, 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(pending) != 2 || pending[0].ID != older.ID {
		t.Fatalf("order = [%s,...], want oldest %s first", pending[0].ID, older.ID)
	}

	if err := store.MarkSent(ctx, older.ID, "2026-06-05T10:00:02.000Z"); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	if err := store.MarkQuarantined(ctx, newer.ID, "bad envelope"); err != nil {
		t.Fatalf("MarkQuarantined: %v", err)
	}
	if n := len(mustFetch(t, store)); n != 0 {
		t.Fatalf("pending after sent+quarantined = %d, want 0", n)
	}
	assertStatus(t, db, older.ID, "sent")
	assertStatus(t, db, newer.ID, "quarantined")

	retry := sampleRow("77777777-7777-7777-8777-777777777777")
	if err := WithinTx(ctx, db, func(tx *sql.Tx) error { return store.Enqueue(tx, retry) }); err != nil {
		t.Fatalf("enqueue retry: %v", err)
	}
	if err := store.RecordFailure(ctx, retry.ID, "broker unreachable"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	p := mustFetch(t, store)
	if len(p) != 1 || p[0].ID != retry.ID || p[0].Attempts != 1 || p[0].LastError == "" {
		t.Fatalf("retry row = %+v, want only %s pending with attempts=1 and an error", p, retry.ID)
	}
}

// The inbox dedupes on envelope id within the caller's tx (idempotent consume).
func TestInboxDedupe(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	const id = "88888888-8888-7888-8888-888888888888"

	err := WithinTx(ctx, db, func(tx *sql.Tx) error {
		seen, herr := InboxHasSeen(ctx, tx, id)
		if herr != nil {
			return herr
		}
		if seen {
			t.Fatal("first sight must not be seen")
		}
		return InboxMarkSeen(ctx, tx, id, "some.event", "2026-06-05T10:00:00.000Z")
	})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}

	err = WithinTx(ctx, db, func(tx *sql.Tx) error {
		seen, herr := InboxHasSeen(ctx, tx, id)
		if herr != nil {
			return herr
		}
		if !seen {
			t.Fatal("redelivery must be seen (deduped)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("redelivery apply: %v", err)
	}
}

func mustFetch(t *testing.T, store *OutboxStore) []OutboxRow {
	t.Helper()
	rows, err := store.FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	return rows
}

func countOutbox(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox").Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
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

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRowContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("querying sqlite_master for %q: %v", name, err)
	}
	return got == name
}
