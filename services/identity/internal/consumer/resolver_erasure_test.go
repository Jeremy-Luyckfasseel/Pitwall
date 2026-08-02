package consumer_test

import (
	"context"
	"database/sql"
	"testing"

	gopdb "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/persistence"
)

// newResolverWithErasure wires the REAL ErasureStore/HeldLookupStore (Task 2) onto the
// resolver's new suppression-gate seam, so these tests exercise the actual persistence
// layer the gate depends on, not a fake.
func newResolverWithErasure(t *testing.T) (*sql.DB, *consumer.TxResolver, *persistence.ErasureStore) {
	t.Helper()
	db, r, _ := newResolver(t)
	es := persistence.NewErasureStore()
	hs := persistence.NewHeldLookupStore()
	r.IsEmailSuppressed = func(ctx context.Context, tx *sql.Tx, hash string) (bool, error) {
		return es.IsEmailSuppressed(ctx, tx, hash)
	}
	r.RecordHeld = func(ctx context.Context, tx *sql.Tx, requestID, hash, occurredAt, recordedAt string) error {
		return hs.Record(ctx, tx, requestID, hash, "email suppressed by prior erasure", occurredAt, recordedAt)
	}
	return db, r, es
}

func suppressEmail(t *testing.T, db *sql.DB, es *persistence.ErasureStore, email string) {
	t.Helper()
	hash := domain.HashEmail(domain.NormalizeEmail(email))
	if err := gopdb.WithinTx(context.Background(), db, func(tx *sql.Tx) error {
		return es.DeleteSlice(context.Background(), tx, "some-erased-master-id", hash, "2026-08-01T11:30:02.140Z")
	}); err != nil {
		t.Fatalf("seed suppression: %v", err)
	}
}

// AC2: a lookup for an email suppressed by a prior erasure is HELD — never minted, never
// replied — and durably recorded.
func TestTxResolver_SuppressedEmailIsHeldNotMinted(t *testing.T) {
	db, r, es := newResolverWithErasure(t)
	suppressEmail(t, db, es, "erased@example.com")

	res, err := r.Resolve(context.Background(),
		incoming("018f9e2a-7c3d-7b21-9c4e-000000000009", "erased@example.com"),
		data("erased@example.com"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Held {
		t.Fatalf("res = %+v; want Held=true for a suppressed email", res)
	}
	if res.MasterID != "" || res.Minted {
		t.Fatalf("a held lookup must not mint: res = %+v", res)
	}
	if n := identityCount(t, db); n != 0 {
		t.Fatalf("identities = %d; want 0 (never minted)", n)
	}
	if rows := pendingOutbox(t, db); len(rows) != 0 {
		t.Fatalf("outbox pending = %d; want 0 (no identity.resolved reply)", len(rows))
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM held_lookups`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("held_lookups rows = %d; want 1", n)
	}
}

// A redelivery of the SAME held envelope must dedupe via the inbox (Duplicate), never a
// second Held/held_lookups row — the inbox-mark inside the held branch must have committed.
func TestTxResolver_RedeliveredHeldLookupIsDuplicateNotHeldAgain(t *testing.T) {
	db, r, es := newResolverWithErasure(t)
	suppressEmail(t, db, es, "erased@example.com")
	env := incoming("018f9e2a-7c3d-7b21-9c4e-000000000009", "erased@example.com")

	first, err := r.Resolve(context.Background(), env, data("erased@example.com"))
	if err != nil || !first.Held {
		t.Fatalf("first Resolve = %+v, err=%v; want Held=true", first, err)
	}
	second, err := r.Resolve(context.Background(), env, data("erased@example.com"))
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if !second.Duplicate || second.Held {
		t.Fatalf("redelivered held lookup = %+v; want Duplicate=true, Held=false", second)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM held_lookups`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("held_lookups rows after redelivery = %d; want 1 (no second hold)", n)
	}
}

// Review finding (code review 2026-08-02): Resolve must defensively re-normalize the
// email before hashing, so a caller that (hypothetically) forgot to normalize first
// still hits the suppression gate — a mismatched hash would silently defeat AC2.
func TestTxResolver_SuppressedEmailHeldEvenWithUnnormalizedCaseAndWhitespace(t *testing.T) {
	db, r, es := newResolverWithErasure(t)
	suppressEmail(t, db, es, "case-erased@example.com")

	// data.Email deliberately NOT pre-normalized here, unlike every other test in this
	// file — proves Resolve's own defensive NormalizeEmail call, not just the caller's.
	res, err := r.Resolve(context.Background(),
		incoming("018f9e2a-7c3d-7b21-9c4e-00000000000a", "  Case-Erased@Example.COM  "),
		domain.LookupData{RequestID: "aa11bb22-cc33-4dd4-8ee5-ff6677889900", Email: "  Case-Erased@Example.COM  "})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Held {
		t.Fatalf("res = %+v; want Held=true for a case/whitespace variant of a suppressed email", res)
	}
	if n := identityCount(t, db); n != 0 {
		t.Fatalf("identities = %d; want 0 (never minted)", n)
	}
}

// Regression guard (byte-identical pre-2.6 behavior): a lookup for a NEVER-erased email
// still mints normally through the suppression-gate-equipped resolver.
func TestTxResolver_NonSuppressedEmailStillMintsNormally(t *testing.T) {
	db, r, _ := newResolverWithErasure(t)
	res, err := r.Resolve(context.Background(),
		incoming("018f9e2a-7c3d-7b21-9c4e-000000000010", "never-erased@example.com"),
		data("never-erased@example.com"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Held {
		t.Fatal("a never-erased email must not be Held")
	}
	if !res.Minted {
		t.Fatal("a never-erased, unknown email must still mint")
	}
	if n := identityCount(t, db); n != 1 {
		t.Fatalf("identities = %d; want 1", n)
	}
	if rows := pendingOutbox(t, db); len(rows) != 1 {
		t.Fatalf("outbox pending = %d; want 1 (normal identity.resolved reply)", len(rows))
	}
}
