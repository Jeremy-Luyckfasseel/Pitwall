// Package persistence is Leaderboard's database facade over the shared persistence
// blueprint mechanics in libs/go-pitwall/persistence. The SQLite open/pragmas, the
// goose migration runner, the WithinTx seam and the idempotent inbox helpers now live
// ONCE in the library (Story 2.1); this package owns Leaderboard's migrations (inbox +
// the session-keyed standings projection) and the read-model store (store.go),
// re-exporting the generic types so existing call sites keep working unchanged.
package persistence

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	gopdb "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
)

// migrationsFS holds Leaderboard's versioned schema-as-code migrations, embedded into
// the binary. New migrations are added as 000N_*.sql files alongside this package.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (creating if absent) the SQLite database at dbPath, applies the standard
// connection pragmas, and runs Leaderboard's migrations to the latest version. It is
// the single startup entrypoint for persistence.
func Open(ctx context.Context, dbPath string) (*sql.DB, error) {
	db, err := gopdb.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	if err := gopdb.Migrate(ctx, db, migrationsFS, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate leaderboard db: %w", err)
	}
	return db, nil
}

// WithinTx runs fn inside a single transaction (commit on nil, rollback otherwise) —
// the seam the atomic CONSUME rests on (inbox dedupe-check + projection upsert + inbox
// insert all commit together).
func WithinTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	return gopdb.WithinTx(ctx, db, fn)
}
