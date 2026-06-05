// Package persistence owns the Timing service's private database: the SQLite
// connection (pure-Go modernc driver, so the CGO_ENABLED=0 static build holds),
// the versioned schema migrations, and the transactional outbox store (Story
// 1.4). Domain tables (laps, sessions) and the event store arrive in later
// stories; this package is the reliability spine's home.
package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// Open opens (creating if absent) the SQLite database at dbPath, applies the
// connection pragmas the outbox relay relies on, and runs migrations to the
// latest version. It is the single startup entrypoint for persistence.
//
// SQLite is a single-writer store; the pool is pinned to one connection so the
// background relay's reads and the producer's writes serialize cleanly with no
// SQLITE_BUSY races. WAL still lets a future reader-heavy path scale without a
// schema change. Callers MUST use the *sql.Tx inside WithinTx (never the *sql.DB
// directly) while a transaction is open, or they will deadlock the single conn.
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
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// dsnFor builds a modernc.org/sqlite DSN with the pragmas applied on every
// connection: WAL journal (reader/writer concurrency), a busy timeout (wait
// rather than fail under contention), and foreign-key enforcement.
func dsnFor(dbPath string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(on)")
	return "file:" + dbPath + "?" + q.Encode()
}

// WithinTx runs fn inside a single transaction, committing if fn returns nil and
// rolling back otherwise. This is the seam the outbox's atomicity rests on: a
// producer writes its domain state AND enqueues the outbox row through the same
// tx, so the two commit together or not at all (Story 1.4 AC1).
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
