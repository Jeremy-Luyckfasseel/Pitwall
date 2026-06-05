package persistence

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// Migrating an empty DB creates the schema; re-running is an idempotent no-op
// (Task 1). Open is the single startup entrypoint: open + pragmas + migrate.
func TestOpenRunsMigrationsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "timing.db")

	db, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if !tableExists(t, db, "outbox") {
		t.Fatalf("outbox table missing after first migrate")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-open the SAME file: goose Up must no-op, not error or duplicate.
	db2, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("second Open (idempotence): %v", err)
	}
	defer db2.Close()
	if !tableExists(t, db2, "outbox") {
		t.Fatalf("outbox table missing after second migrate")
	}
}

// The connection carries the pragmas the relay relies on: WAL (reader/writer
// concurrency) and foreign_keys on.
func TestOpenSetsPragmas(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "timing.db"))
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
