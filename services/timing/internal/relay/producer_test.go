package relay

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
)

// EnqueueEnvelope is the producer seam Story 1.5 plugs laps/sessions into: it
// marshals an envelope and writes a pending outbox row inside the caller's tx,
// so the row is atomic with the domain write (AC1).
func TestEnqueueEnvelope_PersistsPendingRow(t *testing.T) {
	ctx := context.Background()
	db, err := persistence.Open(ctx, filepath.Join(t.TempDir(), "timing.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	store := persistence.NewOutboxStore(db)

	env := messaging.Envelope{
		ID:              "9f1c2b4e-7a3d-4c8e-bf21-5d0a6e2f1a90",
		Type:            "lap.recorded",
		Source:          "timing",
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      messaging.FormatWireTime(time.Unix(0, 0)),
		CorrelationID:   "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		CausationID:     nil,
		Data:            map[string]any{"masterId": "x"},
	}

	if err := persistence.WithinTx(ctx, db, func(tx *sql.Tx) error {
		return EnqueueEnvelope(tx, store, nil, env)
	}); err != nil {
		t.Fatalf("EnqueueEnvelope: %v", err)
	}

	pending, err := store.FetchPending(ctx, 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	got := pending[0]
	if got.ID != env.ID || got.RoutingKey != env.Type || got.CreatedAt != env.OccurredAt {
		t.Fatalf("row = %+v, want id/type/createdAt from the envelope", got)
	}
	// The stored payload is the verbatim, re-readable envelope.
	var back messaging.Envelope
	if err := json.Unmarshal(got.Payload, &back); err != nil {
		t.Fatalf("stored payload not a valid envelope: %v", err)
	}
	if back.Type != "lap.recorded" {
		t.Fatalf("payload type = %q, want lap.recorded", back.Type)
	}
}

// When a validator is supplied, EnqueueEnvelope fails fast inside the producing
// tx — an invalid event never even reaches the outbox (belt-and-suspenders ahead
// of the relay's authoritative validate-on-publish).
func TestEnqueueEnvelope_RejectsInvalidAtEnqueue(t *testing.T) {
	ctx := context.Background()
	db, err := persistence.Open(ctx, filepath.Join(t.TempDir(), "timing.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	store := persistence.NewOutboxStore(db)

	reject := func([]byte) error { return errInvalid }
	bad := messaging.Envelope{ID: "x", Type: "lap.recorded", OccurredAt: "t"}

	err = persistence.WithinTx(ctx, db, func(tx *sql.Tx) error {
		return EnqueueEnvelope(tx, store, reject, bad)
	})
	if err == nil {
		t.Fatalf("expected EnqueueEnvelope to reject an invalid envelope")
	}

	pending, ferr := store.FetchPending(ctx, 10)
	if ferr != nil {
		t.Fatalf("FetchPending: %v", ferr)
	}
	if len(pending) != 0 {
		t.Fatalf("invalid envelope left %d rows; want 0 (never enqueued)", len(pending))
	}
}

var errInvalid = stringError("invalid for test")

type stringError string

func (e stringError) Error() string { return string(e) }
