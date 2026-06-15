package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// Migrate brings the database schema up to the latest version using goose, reading
// the versioned schema-as-code migrations from the service-supplied fsys at dir
// (typically an embed.FS of `migrations/*.sql` and dir "migrations"). It is
// idempotent: goose records applied versions in its own table and re-running Up is a
// no-op. The migrations themselves are service-owned (each service defines its own
// outbox/inbox/domain tables); this is only the generic runner.
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS, dir string) error {
	goose.SetBaseFS(fsys)
	goose.SetLogger(goose.NopLogger()) // structured logging is the service's job; goose stays quiet
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, dir); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
