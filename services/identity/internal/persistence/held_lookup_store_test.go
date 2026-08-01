package persistence_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/persistence"
)

// TestRecordHeldLookup_Persists (AC2): a held lookup is durably captured — never
// dropped — inside the caller's transaction (unlike Story 2.5's HeldLineScanStore, this
// must be atomic with the inbox-mark, see resolver.go).
func TestRecordHeldLookup_Persists(t *testing.T) {
	db, _ := openTestDB(t)
	hs := persistence.NewHeldLookupStore()

	withTx(t, db, func(tx *sql.Tx) error {
		return hs.Record(context.Background(), tx, fixtureRequestID, fixtureHash,
			"email suppressed by prior erasure", fixtureAt, fixtureAt)
	})

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM held_lookups WHERE request_id = ?`, fixtureRequestID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("held_lookups rows = %d; want 1", n)
	}
}

// TestRecordHeldLookup_RepeatedHoldIsTwoRows: no dedupe — every held attempt (even a
// second genuinely-new lookup for the same suppressed email) is its own audit row,
// mirroring HeldLineScanStore's "every scan attempt is its own row" contract.
func TestRecordHeldLookup_RepeatedHoldIsTwoRows(t *testing.T) {
	db, _ := openTestDB(t)
	hs := persistence.NewHeldLookupStore()

	withTx(t, db, func(tx *sql.Tx) error {
		return hs.Record(context.Background(), tx, fixtureRequestID, fixtureHash, "reason", fixtureAt, fixtureAt)
	})
	withTx(t, db, func(tx *sql.Tx) error {
		return hs.Record(context.Background(), tx, "a-different-request-id", fixtureHash, "reason", fixtureAt, fixtureAt)
	})

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM held_lookups WHERE email_hash = ?`, fixtureHash).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("held_lookups rows for the hash = %d; want 2 (no dedupe)", n)
	}
}

const fixtureRequestID = "e9b4f3d5-2a6c-4b8a-bf43-5d7e9a1b3c45"
