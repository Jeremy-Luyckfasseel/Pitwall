package persistence

import (
	"database/sql"

	gopdb "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
)

// OutboxRow / OutboxStore are the transactional outbox mechanics, defined once in
// libs/go-pitwall/persistence and re-exported here so Timing's relay and producer
// keep using persistence.OutboxRow / persistence.OutboxStore unchanged. The outbox
// table DDL lives in this service's migrations (0001_outbox.sql).
type OutboxRow = gopdb.OutboxRow

// OutboxStore is the persistence-side of the transactional outbox.
type OutboxStore = gopdb.OutboxStore

// NewOutboxStore binds the store to an already-open, already-migrated database.
func NewOutboxStore(db *sql.DB) *OutboxStore { return gopdb.NewOutboxStore(db) }
