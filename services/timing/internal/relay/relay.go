// Package relay is Timing's facade over the shared outbox-relay mechanics in
// libs/go-pitwall/relay (Story 2.1). The drain loop, the validate→publish→mark-sent
// lifecycle, and the producer enqueue seam now live once in the library; this package
// re-exports them so Timing's main and simulator keep using relay.New / relay.Config /
// relay.NewEnqueuer unchanged.
package relay

import (
	"context"
	"database/sql"

	librelay "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/relay"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
)

// Store is the outbox persistence the relay drives.
type Store = librelay.Store

// Validator returns a non-nil error if a payload must NOT be published.
type Validator = librelay.Validator

// Publisher publishes body to routingKey, returning nil only on a confirmed ack.
type Publisher = librelay.Publisher

// Config wires the relay's dependencies.
type Config = librelay.Config

// Relay publishes outbox rows. Construct it with New.
type Relay = librelay.Relay

// Kicker is signalled after a successful enqueue so the relay publishes promptly.
type Kicker = librelay.Kicker

// New builds a relay (buffered kick channel for prompt publication).
func New(cfg Config) *Relay { return librelay.New(cfg) }

// EnqueueEnvelope marshals a fully-built envelope and writes a pending outbox row using
// the caller's transaction (atomic with the domain write).
func EnqueueEnvelope(tx *sql.Tx, store *persistence.OutboxStore, validate func([]byte) error, env messaging.Envelope) error {
	return librelay.EnqueueEnvelope(tx, store, validate, env)
}

// NewEnqueuer returns the production producer seam: commit one outbox row in its own
// transaction (atomic with any domain write) and kick the relay for prompt publish.
func NewEnqueuer(db *sql.DB, store *persistence.OutboxStore, validate func([]byte) error, kicker Kicker) func(context.Context, messaging.Envelope) error {
	return librelay.NewEnqueuer(db, store, validate, kicker)
}
