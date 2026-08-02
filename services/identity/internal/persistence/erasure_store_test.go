package persistence_test

import (
	"context"
	"database/sql"
	"testing"

	gopdb "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/persistence"
)

const (
	fixtureMasterID = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
	fixtureHash     = "deadbeefcafebabe0000000000000000000000000000000000000000000000"
	fixtureAt       = "2026-08-01T11:30:02.140Z"
)

func withTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx) error) {
	t.Helper()
	if err := gopdb.WithinTx(context.Background(), db, fn); err != nil {
		t.Fatalf("WithinTx: %v", err)
	}
}

// TestDeleteSlice_RemovesRowAndSuppressesEmail (AC1): erasing a known masterId removes
// its identities row AND writes the (already-hashed) email into the suppression table —
// in one transaction.
func TestDeleteSlice_RemovesRowAndSuppressesEmail(t *testing.T) {
	db, store := openTestDB(t)
	resolveOrMint(t, db, store, "jeremy@example.com", fixtureMasterID, fixtureAt)

	es := persistence.NewErasureStore()
	withTx(t, db, func(tx *sql.Tx) error {
		return es.DeleteSlice(context.Background(), tx, fixtureMasterID, fixtureHash, fixtureAt)
	})

	if countIdentities(t, db) != 0 {
		t.Fatalf("identities row count = %d; want 0 (deleted)", countIdentities(t, db))
	}
	withTx(t, db, func(tx *sql.Tx) error {
		suppressed, err := es.IsEmailSuppressed(context.Background(), tx, fixtureHash)
		if err != nil {
			t.Fatalf("IsEmailSuppressed: %v", err)
		}
		if !suppressed {
			t.Fatal("email hash not suppressed after DeleteSlice")
		}
		return nil
	})
}

// TestDeleteSlice_AlreadyGoneIsGracefulNoOp: a second erasure for an already-erased
// masterId (e.g. a duplicate request with a different envelope id) must not error.
func TestDeleteSlice_AlreadyGoneIsGracefulNoOp(t *testing.T) {
	db, _ := openTestDB(t)
	es := persistence.NewErasureStore()
	withTx(t, db, func(tx *sql.Tx) error {
		return es.DeleteSlice(context.Background(), tx, "never-existed", fixtureHash, fixtureAt)
	})
	// no panic/error = pass; nothing to assert beyond successful completion
}

// TestLookupEmail_FoundAndNotFound (AC1/AC2): the email lookup drives the caller's
// hash-then-delete sequencing (main.go wiring) — found=false must not be an error.
func TestLookupEmail_FoundAndNotFound(t *testing.T) {
	db, store := openTestDB(t)
	resolveOrMint(t, db, store, "jeremy@example.com", fixtureMasterID, fixtureAt)

	es := persistence.NewErasureStore()
	var email string
	var found bool
	var err error
	withTx(t, db, func(tx *sql.Tx) error {
		email, found, err = es.LookupEmail(context.Background(), tx, fixtureMasterID)
		return err
	})
	if !found || email != "jeremy@example.com" {
		t.Fatalf("LookupEmail = (%q, %v); want (jeremy@example.com, true)", email, found)
	}

	withTx(t, db, func(tx *sql.Tx) error {
		_, found, err = es.LookupEmail(context.Background(), tx, "no-such-master-id")
		return err
	})
	if found {
		t.Fatal("LookupEmail found=true for a masterId that was never minted")
	}
}

// TestWriteTombstone_IdempotentInsertOrIgnore (AC1): a tombstone is written once and a
// repeat write (idempotent double-erasure) is a graceful no-op, not a duplicate row/error.
func TestWriteTombstone_IdempotentInsertOrIgnore(t *testing.T) {
	db, _ := openTestDB(t)
	es := persistence.NewErasureStore()

	withTx(t, db, func(tx *sql.Tx) error {
		return es.WriteTombstone(context.Background(), tx, fixtureMasterID, fixtureAt)
	})
	withTx(t, db, func(tx *sql.Tx) error {
		return es.WriteTombstone(context.Background(), tx, fixtureMasterID, fixtureAt)
	})

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM identity_tombstones WHERE master_id = ?`, fixtureMasterID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("identity_tombstones rows = %d; want 1 (INSERT OR IGNORE)", n)
	}
}

// TestIsEmailSuppressed_FalseBeforeSuppression: the negative case, so the resolver's
// happy path (a never-erased email) is provably unaffected.
func TestIsEmailSuppressed_FalseBeforeSuppression(t *testing.T) {
	db, _ := openTestDB(t)
	es := persistence.NewErasureStore()
	withTx(t, db, func(tx *sql.Tx) error {
		suppressed, err := es.IsEmailSuppressed(context.Background(), tx, fixtureHash)
		if err != nil {
			t.Fatal(err)
		}
		if suppressed {
			t.Fatal("suppressed = true before any erasure ran")
		}
		return nil
	})
}
