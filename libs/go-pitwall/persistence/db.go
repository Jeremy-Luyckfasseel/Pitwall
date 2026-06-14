// Package persistence holds the shared database blueprint mechanics every Pitwall Go
// service reuses: opening a private SQLite store with the standard connection pragmas,
// running goose migrations from a service-supplied migrations FS, the WithinTx
// transaction seam, the transactional outbox store, and the idempotent inbox. It
// carries mechanics ONLY — no domain tables or projections (those live in each
// service, defined by that service's own migrations).
package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// Open opens (creating if absent) the SQLite database at dbPath and applies the
// standard connection pragmas. It does NOT run migrations — the caller supplies its
// own migrations FS and calls Migrate (migrations are service-owned).
//
// SQLite is a single-writer store; the pool is pinned to one connection so a
// background relay's reads and the producer's writes serialize cleanly with no
// SQLITE_BUSY races. WAL still lets a future reader-heavy path scale without a schema
// change. Callers MUST use the *sql.Tx inside WithinTx (never the *sql.DB directly)
// while a transaction is open, or they will deadlock the single conn.
func Open(ctx context.Context, dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsnFor(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open sqlite at %q: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite at %q: %w", dbPath, err)
	}
	return db, nil
}

// dsnFor builds a modernc.org/sqlite DSN with the pragmas applied on every
// connection: WAL journal (reader/writer concurrency), a busy timeout (wait rather
// than fail under contention), and foreign-key enforcement.
func dsnFor(dbPath string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(on)")
	return "file:" + dbPath + "?" + q.Encode()
}

// WithinTx runs fn inside a single transaction, committing if fn returns nil and
// rolling back otherwise. This is the seam the outbox's atomicity (and the inbox's
// dedupe-with-write) rests on: a service writes its domain state AND its outbox/inbox
// row through the same tx, so the two commit together or not at all.
func WithinTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
