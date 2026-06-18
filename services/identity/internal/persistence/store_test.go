package persistence_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	gopdb "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/persistence"
)

func openTestDB(t *testing.T) (*sql.DB, *persistence.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "identity-test.db")
	db, err := persistence.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, persistence.NewStore(db)
}

// resolveOrMint is the test seam: run the store call inside one tx, as production does.
func resolveOrMint(t *testing.T, db *sql.DB, store *persistence.Store, email, candidate, now string) (master string, minted bool) {
	t.Helper()
	err := gopdb.WithinTx(context.Background(), db, func(tx *sql.Tx) error {
		var e error
		master, minted, e = store.ResolveOrMint(context.Background(), tx, email, candidate, now)
		return e
	})
	if err != nil {
		t.Fatalf("ResolveOrMint(%q): %v", email, err)
	}
	return master, minted
}

func TestResolveOrMint_MintsUnknownEmail(t *testing.T) {
	db, store := openTestDB(t)
	got, minted := resolveOrMint(t, db, store, "jeremy@example.com", "id-A", "2026-06-15T09:14:02.200Z")
	if !minted {
		t.Fatalf("minted = false; want true for an unknown email")
	}
	if got != "id-A" {
		t.Fatalf("masterId = %q; want the minted candidate id-A", got)
	}
}

func TestResolveOrMint_ReusesKnownEmail(t *testing.T) {
	db, store := openTestDB(t)
	first, _ := resolveOrMint(t, db, store, "jeremy@example.com", "id-A", "2026-06-15T09:14:02.200Z")
	// A SECOND lookup for the same email, offering a DIFFERENT candidate id, must
	// return the FIRST id and NOT mint (no duplicate, no isNew). The candidate id-B
	// is discarded.
	second, minted := resolveOrMint(t, db, store, "jeremy@example.com", "id-B", "2026-06-15T10:00:00.000Z")
	if minted {
		t.Fatalf("minted = true on a known email; want false (reuse)")
	}
	if second != first {
		t.Fatalf("masterId = %q; want the existing %q (one canonical id per person)", second, first)
	}
	if countIdentities(t, db) != 1 {
		t.Fatalf("identities row count = %d; want exactly 1 (no duplicate)", countIdentities(t, db))
	}
}

func TestResolveOrMint_DistinctEmailsGetDistinctIds(t *testing.T) {
	db, store := openTestDB(t)
	a, _ := resolveOrMint(t, db, store, "a@example.com", "id-A", "2026-06-15T09:00:00.000Z")
	b, mintedB := resolveOrMint(t, db, store, "b@example.com", "id-B", "2026-06-15T09:00:01.000Z")
	if !mintedB || a == b {
		t.Fatalf("distinct emails resolved to (%q,%q); want two distinct minted ids", a, b)
	}
	if countIdentities(t, db) != 2 {
		t.Fatalf("identities row count = %d; want 2", countIdentities(t, db))
	}
}

// Simulates the race loser: a row for the email is already committed by a "winner";
// our candidate INSERT hits the UNIQUE(email) conflict, is a no-op, and we return the
// winner's id with minted=false. This is the single-writer guarantee at the storage
// layer (the unique constraint), exercised deterministically.
func TestResolveOrMint_ConflictReturnsWinnerNotSecondMint(t *testing.T) {
	db, store := openTestDB(t)
	winner, minted := resolveOrMint(t, db, store, "race@example.com", "id-WINNER", "2026-06-15T09:00:00.000Z")
	if !minted || winner != "id-WINNER" {
		t.Fatalf("setup: winner = (%q, %v); want (id-WINNER, true)", winner, minted)
	}
	loser, loserMinted := resolveOrMint(t, db, store, "race@example.com", "id-LOSER", "2026-06-15T09:00:00.500Z")
	if loserMinted {
		t.Fatalf("loser minted = true; want false (UNIQUE(email) blocks a second mint)")
	}
	if loser != "id-WINNER" {
		t.Fatalf("loser got %q; want the winner's id-WINNER", loser)
	}
}

func countIdentities(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM identities`).Scan(&n); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	return n
}
