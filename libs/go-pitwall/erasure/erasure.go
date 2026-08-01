// Package erasure provides the reusable right-to-be-forgotten mechanics every Pitwall
// service shares (DG-3 / DG-7, ADR-0009): on a validated privacy.erasure_requested it
// runs, atomically, inbox-dedupe → the service's delete-slice → a tombstone write →
// inbox-mark, then builds the privacy.erased acknowledgement to emit. It also offers a
// tombstone guard so a replayed event cannot resurrect an erased slice.
//
// It carries blueprint mechanics ONLY: each service injects its own delete-slice and
// tombstone callbacks (it owns those tables); the library hard-codes no schema. This
// prevents ten hand-rolled, divergent tombstone implementations.
package erasure

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
)

// Event types (cross-cutting privacy events; every service consumes the request and
// emits the ack — ADR-0009).
const (
	ErasureRequestedType = "privacy.erasure_requested"
	ErasedType           = "privacy.erased"
)

// Mode records how a service honored erasure: deleted = local slice removed;
// anonymized = a retained record stripped of PII (e.g. Billing, under legal retention).
type Mode string

const (
	ModeDeleted    Mode = "deleted"
	ModeAnonymized Mode = "anonymized"
)

// SliceDeleter deletes/anonymizes the service's OWN slice for masterID, inside the
// caller's transaction. The only domain-specific piece — supplied by the service.
type SliceDeleter func(ctx context.Context, tx *sql.Tx, masterID string) error

// TombstoneWriter records a tombstone for masterID inside the caller's transaction, so
// a replayed event cannot resurrect the slice. Service-supplied (it owns the table).
type TombstoneWriter func(ctx context.Context, tx *sql.Tx, masterID string) error

// TombstoneChecker reports whether masterID is tombstoned. Service-supplied (reads its
// own tombstone table). Used by GuardedCreate.
type TombstoneChecker func(ctx context.Context, masterID string) (bool, error)

// AckEnqueuer durably enqueues the privacy.erased ack INSIDE the caller's transaction
// (e.g. via libs/go-pitwall/relay.EnqueueEnvelope against the service's own outbox), so
// the ack commits atomically with the delete + tombstone. Required — without it a crash
// between Handle committing and an out-of-band publish would permanently lose the ack
// (the inbox is already marked seen, so a redelivery reports Duplicate and emits
// nothing), violating the "never silently drop" rule the whole erasure saga depends on.
type AckEnqueuer func(ctx context.Context, tx *sql.Tx, ack messaging.Envelope) error

// requestData is the tolerant-reader view of a privacy.erasure_requested payload — the
// scaffold needs the requestId (for the ack) and the masterId (to erase).
type requestData struct {
	RequestID string `json:"requestId"`
	MasterID  string `json:"masterId"`
}

// erasedData is the privacy.erased payload (contract/schemas/privacy/privacy.erased.v1).
type erasedData struct {
	RequestID string `json:"requestId"`
	MasterID  string `json:"masterId"`
	Service   string `json:"service"`
	Mode      string `json:"mode"`
	At        string `json:"at"`
}

// Handler orchestrates the reusable erasure mechanics for one service. Construct it
// with the service's DB, name, optional Mode (defaults to deleted), and the two
// delete-slice / tombstone callbacks.
type Handler struct {
	DB        *sql.DB
	Service   string
	Mode      Mode             // defaults to ModeDeleted
	Delete    SliceDeleter     // required
	Tombstone TombstoneWriter  // required
	Enqueue   AckEnqueuer      // required — atomic ack enqueue (see AckEnqueuer doc)
	Now       func() time.Time // injectable clock (defaults to time.Now)
}

// Result reports the outcome of handling one erasure request.
type Result struct {
	RequestID string
	MasterID  string
	Duplicate bool               // the envelope id was already processed (a no-op redelivery)
	Ack       messaging.Envelope // the privacy.erased envelope to emit (zero value on a duplicate)
}

// Handle consumes one validated privacy.erasure_requested envelope. Within ONE
// transaction it dedupes on the envelope id (idempotent inbox), runs the service's
// delete-slice, writes the tombstone, and records the inbox row — so a crash can
// neither half-erase nor ack-without-erasing. On first sight it returns the
// privacy.erased ack for the caller to publish (to the service's own exchange, via the
// outbox); on a redelivery it returns Duplicate and an empty ack. The caller must have
// already validated the envelope against /contract.
func (h *Handler) Handle(ctx context.Context, env messaging.IncomingEnvelope) (Result, error) {
	var d requestData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return Result{}, fmt.Errorf("decode erasure request: %w", err)
	}
	if d.MasterID == "" {
		return Result{}, fmt.Errorf("erasure request %s has empty masterId", env.ID)
	}
	now := h.Now
	if now == nil {
		now = time.Now
	}
	at := messaging.FormatWireTime(now())

	mode := h.Mode
	if mode == "" {
		mode = ModeDeleted
	}
	ack := messaging.NewCausedEnvelope(ErasedType, h.Service, env.CorrelationID, env.ID, at, erasedData{
		RequestID: d.RequestID,
		MasterID:  d.MasterID,
		Service:   h.Service,
		Mode:      string(mode),
		At:        at,
	})

	var duplicate bool
	err := persistence.WithinTx(ctx, h.DB, func(tx *sql.Tx) error {
		seen, e := persistence.InboxHasSeen(ctx, tx, env.ID)
		if e != nil {
			return e
		}
		if seen {
			duplicate = true
			return nil
		}
		if e := h.Delete(ctx, tx, d.MasterID); e != nil {
			return fmt.Errorf("delete slice for %s: %w", d.MasterID, e)
		}
		if e := h.Tombstone(ctx, tx, d.MasterID); e != nil {
			return fmt.Errorf("write tombstone for %s: %w", d.MasterID, e)
		}
		// Enqueue the ack INSIDE this same transaction (AckEnqueuer doc) so it commits
		// atomically with the delete + tombstone — a crash after this point either takes
		// everything (including the durably-queued ack) or nothing.
		if e := h.Enqueue(ctx, tx, ack); e != nil {
			return fmt.Errorf("enqueue privacy.erased ack for %s: %w", d.MasterID, e)
		}
		return persistence.InboxMarkSeen(ctx, tx, env.ID, env.Type, at)
	})
	if err != nil {
		return Result{}, err
	}
	if duplicate {
		return Result{RequestID: d.RequestID, MasterID: d.MasterID, Duplicate: true}, nil
	}

	return Result{RequestID: d.RequestID, MasterID: d.MasterID, Ack: ack}, nil
}

// GuardedCreate runs create ONLY if masterID is not tombstoned; it returns blocked=true
// (create skipped) if it is. This is the reusable guard a consumer wraps its slice
// (re)creation in so a late/replayed event cannot silently resurrect an erased id
// (DG-7). The tombstone check + the create should ordinarily share one transaction in
// the caller; this helper expresses the decision so every service applies it the same
// way.
func GuardedCreate(ctx context.Context, check TombstoneChecker, masterID string, create func() error) (blocked bool, err error) {
	tombstoned, err := check(ctx, masterID)
	if err != nil {
		return false, fmt.Errorf("tombstone check for %s: %w", masterID, err)
	}
	if tombstoned {
		return true, nil
	}
	return false, create()
}
