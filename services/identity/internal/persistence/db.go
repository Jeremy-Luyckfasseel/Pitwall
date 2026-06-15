// Package persistence is Identity's database facade over the shared persistence
// blueprint mechanics in libs/go-pitwall/persistence (Story 2.1). The SQLite
// open/pragmas, the goose migration runner, the WithinTx seam, the transactional
// outbox store and the idempotent inbox helpers live ONCE in the library; this
// package owns Identity's migrations (the identities table + inbox + outbox) and the
// resolve-or-mint store (store.go), re-exporting the generic types so the consumer
// and relay call sites stay idiomatic.
package persistence

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	gopdb "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
)

// migrationsFS holds Identity's versioned schema-as-code migrations, embedded into the
// binary. New migrations are added as 000N_*.sql files alongside this package.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (creating if absent) the SQLite database at dbPath, applies the standard
// connection pragmas, and runs Identity's migrations to the latest version. It is the
// single startup entrypoint for persistence.
func Open(ctx context.Context, dbPath string) (*sql.DB, error) {
	db, err := gopdb.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	if err := gopdb.Migrate(ctx, db, migrationsFS, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate identity db: %w", err)
	}
	return db, nil
}

// WithinTx runs fn inside a single transaction (commit on nil, rollback otherwise) —
// the seam the atomic CONSUME rests on: the inbox dedupe-check + resolve-or-mint + the
// identity.resolved outbox enqueue + the inbox insert all commit together or not at all.
func WithinTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	return gopdb.WithinTx(ctx, db, fn)
}

// NewOutboxStore re-exports the generic transactional outbox store bound to db, so the
// relay wiring (cmd/identity) reads idiomatically.
func NewOutboxStore(db *sql.DB) *gopdb.OutboxStore {
	return gopdb.NewOutboxStore(db)
}
