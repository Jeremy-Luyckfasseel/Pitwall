// Package persistence owns the Leaderboard service's private database: the SQLite
// connection (pure-Go modernc driver, so the CGO_ENABLED=0 static build holds),
// the versioned schema migrations, and the read-model stores — a durable
// idempotent inbox (dedupe on envelope id, M6) and the best-lap standings
// projection. The projection owns NO canonical state (FR41: a pure fold of
// consumed events, fully rebuildable). The pattern mirrors
// services/timing/internal/persistence; libs/go-pitwall extraction is Story 2.1.
package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// Open opens (creating if absent) the SQLite database at dbPath, applies the
// connection pragmas, and runs migrations to the latest version. It is the
// single startup entrypoint for persistence.
//
// SQLite is a single-writer store; the pool is pinned to one connection so the
// consumer's writes and the web layer's reads serialize cleanly with no
// SQLITE_BUSY races. Callers MUST use the *sql.Tx inside WithinTx (never the
// *sql.DB directly) while a transaction is open, or they will deadlock the conn.
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
// connection: WAL journal, a busy timeout, and foreign-key enforcement.
func dsnFor(dbPath string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(on)")
	return "file:" + dbPath + "?" + q.Encode()
}

// WithinTx runs fn inside a single transaction, committing if fn returns nil and
// rolling back otherwise. This is the seam the atomic CONSUME rests on: the
// inbox dedupe-check, the projection upsert, and the inbox insert all commit
// together or not at all, so a crash can neither double-apply nor apply-without-
// marking (the consumer-side mirror of Timing's transactional outbox).
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
