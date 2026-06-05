package relay

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
)

// EnqueueEnvelope is the producer-side seam: it marshals a fully-built envelope
// and writes a pending outbox row using the caller's transaction, so the row
// commits atomically with whatever domain state the same tx wrote (Story 1.4
// AC1). Story 1.5's simulator builds a lap.recorded/session.* envelope and calls
// this inside its persist-the-lap transaction.
//
// If validate is non-nil, the marshalled envelope is checked BEFORE the insert,
// so an invalid event fails fast in the producing tx and never reaches the
// outbox — belt-and-suspenders ahead of the relay's authoritative
// validate-on-publish (AC2). Pass nil to skip (e.g. when the caller has already
// validated).
//
// The envelope is stored verbatim — already in canonical wire form (pinned
// timestamps, camelCase) — so the relay republishes the exact bytes without
// re-serializing. The row's created_at is the envelope's occurredAt, giving the
// relay a stable oldest-first publish order.
func EnqueueEnvelope(tx *sql.Tx, store *persistence.OutboxStore, validate func([]byte) error, env messaging.Envelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope %s: %w", env.ID, err)
	}
	if validate != nil {
		if verr := validate(payload); verr != nil {
			return fmt.Errorf("refusing to enqueue invalid %s (%s): %w", env.Type, env.ID, verr)
		}
	}
	return store.Enqueue(tx, persistence.OutboxRow{
		ID:         env.ID,
		RoutingKey: env.Type,
		Payload:    payload,
		CreatedAt:  env.OccurredAt,
	})
}

// Kicker is signalled after a successful enqueue so the relay publishes the
// fresh row promptly instead of waiting a full poll interval. *relay.Relay
// satisfies it via Kick().
type Kicker interface {
	Kick()
}

// NewEnqueuer returns the production producer seam: a single call that commits
// one outbox row in its own transaction (atomic with any domain write the same
// tx makes) and then kicks the relay for prompt publication. This is what the
// Story-1.5 simulator — and every future domain producer — calls to publish
// durably through the outbox. A failed enqueue (incl. an enqueue-time validation
// rejection) returns the error and does NOT kick.
func NewEnqueuer(db *sql.DB, store *persistence.OutboxStore, validate func([]byte) error, kicker Kicker) func(context.Context, messaging.Envelope) error {
	return func(ctx context.Context, env messaging.Envelope) error {
		if err := persistence.WithinTx(ctx, db, func(tx *sql.Tx) error {
			return EnqueueEnvelope(tx, store, validate, env)
		}); err != nil {
			return err
		}
		kicker.Kick()
		return nil
	}
}
