package consumer_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/persistence"
)

func newResolver(t *testing.T) (*sql.DB, *consumer.TxResolver, *counter) {
	t.Helper()
	contractDir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("locate /contract: %v", err)
	}
	validator, err := messaging.NewValidator(contractDir)
	if err != nil {
		t.Fatalf("compile schemas: %v", err)
	}
	db, err := persistence.Open(context.Background(), filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// The candidate masterIds MUST be lowercase UUID v4 — the reply is validated
	// against /contract on enqueue (ValidateOut), and identity.resolved.masterId pins
	// the strict v4 pattern (AC1, Q&A Round 31).
	mint := &counter{seq: []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}}
	r := &consumer.TxResolver{
		DB:          db,
		Store:       persistence.NewStore(db),
		Outbox:      persistence.NewOutboxStore(db),
		ValidateOut: validator.ValidateEnvelopeBytes,
		MintID:      mint.next,
		Now:         func() string { return "2026-06-15T09:14:02.200Z" },
		Source:      "identity",
		ResolvedKey: messaging.ResolvedRoutingKey,
		// Nothing is suppressed by default — tests that exercise the erasure gate wire
		// their own IsEmailSuppressed/RecordHeld against the real stores
		// (resolver_erasure_test.go's newResolverWithErasure).
		IsEmailSuppressed: func(context.Context, *sql.Tx, string) (bool, error) { return false, nil },
		RecordHeld:        func(context.Context, *sql.Tx, string, string, string, string) error { return nil },
	}
	return db, r, mint
}

type counter struct {
	seq []string
	i   int
}

func (c *counter) next() string {
	id := c.seq[c.i%len(c.seq)]
	c.i++
	return id
}

func incoming(id, email string) messaging.IncomingEnvelope {
	return messaging.IncomingEnvelope{
		ID:            id,
		Type:          messaging.LookupRequestedRoutingKey,
		Source:        "frontend",
		CorrelationID: "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		Data:          json.RawMessage(`{"requestId":"aa11bb22-cc33-4dd4-8ee5-ff6677889900","email":"` + email + `"}`),
	}
}

func data(email string) domain.LookupData {
	return domain.LookupData{RequestID: "aa11bb22-cc33-4dd4-8ee5-ff6677889900", Email: email}
}

func TestTxResolver_MintEnqueuesValidReply(t *testing.T) {
	db, r, _ := newResolver(t)
	res, err := r.Resolve(context.Background(), incoming("018f9e2a-7c3d-7b21-9c4e-000000000001", "a@example.com"), data("a@example.com"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Minted || res.MasterID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("res = %+v; want minted 11111111-1111-4111-8111-111111111111", res)
	}
	if n := identityCount(t, db); n != 1 {
		t.Fatalf("identities = %d; want 1", n)
	}
	// Exactly one pending reply enqueued, correctly correlated.
	rows := pendingOutbox(t, db)
	if len(rows) != 1 {
		t.Fatalf("outbox pending = %d; want 1", len(rows))
	}
	var env messaging.Envelope
	if err := json.Unmarshal(rows[0], &env); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if env.Type != "identity.resolved" || env.Source != "identity" {
		t.Fatalf("reply type/source = %s/%s", env.Type, env.Source)
	}
	if env.CausationID == nil || *env.CausationID != "018f9e2a-7c3d-7b21-9c4e-000000000001" {
		t.Fatalf("reply causationId = %v; want the request id", env.CausationID)
	}
	if env.CorrelationID != "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55" {
		t.Fatalf("reply correlationId not copied: %s", env.CorrelationID)
	}
}

func TestTxResolver_ReuseSameMasterIdSecondReply(t *testing.T) {
	db, r, _ := newResolver(t)
	first, _ := r.Resolve(context.Background(), incoming("018f9e2a-7c3d-7b21-9c4e-000000000001", "a@example.com"), data("a@example.com"))
	second, err := r.Resolve(context.Background(), incoming("018f9e2a-7c3d-7b21-9c4e-000000000002", "a@example.com"), data("a@example.com"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if second.Minted {
		t.Fatal("second lookup for the same email must reuse, not mint")
	}
	if second.MasterID != first.MasterID {
		t.Fatalf("masterId differs: %q vs %q", second.MasterID, first.MasterID)
	}
	if n := identityCount(t, db); n != 1 {
		t.Fatalf("identities = %d; want 1 (no duplicate)", n)
	}
	// Two DISTINCT lookups -> two replies (the requester always gets an answer), both same masterId.
	if rows := pendingOutbox(t, db); len(rows) != 2 {
		t.Fatalf("outbox pending = %d; want 2 replies", len(rows))
	}
}

func TestTxResolver_DuplicateEnvelopeIsNoOp(t *testing.T) {
	db, r, _ := newResolver(t)
	id := "018f9e2a-7c3d-7b21-9c4e-000000000001"
	if _, err := r.Resolve(context.Background(), incoming(id, "a@example.com"), data("a@example.com")); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	res, err := r.Resolve(context.Background(), incoming(id, "a@example.com"), data("a@example.com"))
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if !res.Duplicate {
		t.Fatal("redelivered same envelope id must be a Duplicate no-op")
	}
	if n := identityCount(t, db); n != 1 {
		t.Fatalf("identities = %d; want 1 (no second mint)", n)
	}
	if rows := pendingOutbox(t, db); len(rows) != 1 {
		t.Fatalf("outbox pending = %d; want 1 (no second reply)", len(rows))
	}
}

func identityCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM identities`).Scan(&n); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	return n
}

func pendingOutbox(t *testing.T, db *sql.DB) [][]byte {
	t.Helper()
	store := persistence.NewOutboxStore(db)
	rows, err := store.FetchPending(context.Background(), 100)
	if err != nil {
		t.Fatalf("fetch pending: %v", err)
	}
	out := make([][]byte, 0, len(rows))
	for _, r := range rows {
		if r.RoutingKey != "identity.resolved" {
			t.Fatalf("unexpected outbox routing key %q", r.RoutingKey)
		}
		out = append(out, r.Payload)
	}
	return out
}
