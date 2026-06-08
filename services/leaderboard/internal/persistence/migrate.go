package persistence

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

// migrationsFS holds the versioned schema-as-code migrations, embedded into the
// binary so the container needs no migration files on disk (architecture
// §Migrations: goose, versioned). New migrations are added as 000N_*.sql files.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrate brings the database schema up to the latest version. It is idempotent:
// goose records applied versions in its own table and re-running Up is a no-op.
func migrate(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger()) // structured logging is the service's job; goose stays quiet
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
