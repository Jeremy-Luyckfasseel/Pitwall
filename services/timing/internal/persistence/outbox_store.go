package persistence

import (
	"context"
	"database/sql"
	"fmt"
)

// OutboxRow is one durable outgoing event awaiting (or past) publication.
type OutboxRow struct {
	ID         string // = envelope id
	RoutingKey string // = envelope type; the key published on timing.events
	Payload    []byte // the full, already-marshalled envelope JSON
	Status     string // pending | sent | quarantined
	Attempts   int
	LastError  string
	CreatedAt  string // wire-format timestamp
	SentAt     string // empty until sent
}

// OutboxStore is the persistence-side of the transactional outbox: Enqueue runs
// inside the producer's own transaction (atomic with the domain write); the
// other methods are driven by the background relay.
type OutboxStore struct {
	db *sql.DB
}

// NewOutboxStore binds the store to an already-open, already-migrated database.
func NewOutboxStore(db *sql.DB) *OutboxStore {
	return &OutboxStore{db: db}
}

// Enqueue inserts a pending outbox row using the caller's transaction, so the
// row commits together with whatever domain state the same tx wrote (AC1). The
// row is always inserted pending with zero attempts.
func (s *OutboxStore) Enqueue(tx *sql.Tx, row OutboxRow) error {
	_, err := tx.Exec(
		`INSERT INTO outbox (id, routing_key, payload, status, attempts, created_at)
		 VALUES (?, ?, ?, 'pending', 0, ?)`,
		row.ID, row.RoutingKey, row.Payload, row.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("enqueue outbox row %s: %w", row.ID, err)
	}
	return nil
}

// FetchPending returns up to limit pending rows, oldest-first (created_at), so
// the relay publishes in production order. The id tie-breaker keeps the order
// stable when two rows share a millisecond timestamp.
func (s *OutboxStore) FetchPending(ctx context.Context, limit int) ([]OutboxRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, routing_key, payload, status, attempts, last_error, created_at, COALESCE(sent_at, '')
		   FROM outbox
		  WHERE status = 'pending'
		  ORDER BY created_at ASC, id ASC
		  LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch pending: %w", err)
	}
	defer rows.Close()

	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		if err := rows.Scan(&r.ID, &r.RoutingKey, &r.Payload, &r.Status, &r.Attempts, &r.LastError, &r.CreatedAt, &r.SentAt); err != nil {
			return nil, fmt.Errorf("scan pending row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkSent flips a row to sent after a confirmed broker ack (AC1: sent only
// after ack).
func (s *OutboxStore) MarkSent(ctx context.Context, id, sentAt string) error {
	return s.exec(ctx, "mark sent", id,
		`UPDATE outbox SET status = 'sent', sent_at = ? WHERE id = ?`, sentAt, id)
}

// MarkQuarantined terminally quarantines a row that could not be validated
// against /contract (AC2): it is never published and never retried. This is a
// producer-side quarantine (a local status), distinct from the consumer-side
// RabbitMQ DLQ/parking topology (Story 1.9).
func (s *OutboxStore) MarkQuarantined(ctx context.Context, id, lastErr string) error {
	return s.exec(ctx, "mark quarantined", id,
		`UPDATE outbox SET status = 'quarantined', attempts = attempts + 1, last_error = ? WHERE id = ?`, lastErr, id)
}

// RecordFailure notes a transient publish failure (broker unreachable / nack):
// the row stays pending and is retried on a later tick (AC3).
func (s *OutboxStore) RecordFailure(ctx context.Context, id, lastErr string) error {
	return s.exec(ctx, "record failure", id,
		`UPDATE outbox SET attempts = attempts + 1, last_error = ? WHERE id = ?`, lastErr, id)
}

func (s *OutboxStore) exec(ctx context.Context, what, id, query string, args ...any) error {
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("%s for %s: %w", what, id, err)
	}
	return nil
}
