package erasure

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
)

//go:embed testdata/migrations/*.sql
var testMigrationsFS embed.FS

const (
	fixtureMaster  = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
	fixtureRequest = "e9b4f3d5-2a6c-4b8a-bf43-5d7e9a1b3c45"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := persistence.Open(context.Background(), filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := persistence.Migrate(context.Background(), db, testMigrationsFS, "testdata/migrations"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A service supplies these two domain callbacks; everything else is library mechanics.
func deleteThing(ctx context.Context, tx *sql.Tx, masterID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM things WHERE master_id = ?`, masterID)
	return err
}
func writeTombstone(ctx context.Context, tx *sql.Tx, masterID string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO tombstones (master_id) VALUES (?)`, masterID)
	return err
}
func checker(db *sql.DB) TombstoneChecker {
	return func(ctx context.Context, masterID string) (bool, error) {
		var one int
		err := db.QueryRowContext(ctx, `SELECT 1 FROM tombstones WHERE master_id = ?`, masterID).Scan(&one)
		if err == sql.ErrNoRows {
			return false, nil
		}
		return err == nil, err
	}
}

func incomingRequest(t *testing.T, envID, requestID, masterID string) messaging.IncomingEnvelope {
	t.Helper()
	cid := (*string)(nil)
	env := messaging.Envelope{
		ID:              envID,
		Type:            ErasureRequestedType,
		Source:          "frontend",
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      "2026-06-01T11:30:00.000Z",
		CorrelationID:   requestID,
		CausationID:     cid,
		Data: map[string]any{
			"requestId": requestID, "masterId": masterID,
			"requestedBy": "self", "adminActor": nil, "at": "2026-06-01T11:30:00.000Z",
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	in, err := messaging.DecodeIncoming(b)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func newHandler(db *sql.DB) *Handler {
	return &Handler{
		DB:        db,
		Service:   "driver",
		Delete:    deleteThing,
		Tombstone: writeTombstone,
		Now:       func() time.Time { return time.Date(2026, 6, 1, 11, 30, 2, 140_000_000, time.UTC) },
	}
}

// AC2: the scaffold deletes the slice, writes a tombstone, records the inbox, and
// returns a contract-valid privacy.erased ack.
func TestHandle_DeletesWritesTombstoneAndAcks(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`INSERT INTO things (master_id, payload) VALUES (?, 'pii')`, fixtureMaster); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := newHandler(db)

	res, err := h.Handle(context.Background(), incomingRequest(t, fixtureRequest, fixtureRequest, fixtureMaster))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Duplicate {
		t.Fatal("first handling must not be a duplicate")
	}
	// slice gone
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM things WHERE master_id = ?`, fixtureMaster).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("slice not deleted: %d rows remain", n)
	}
	// tombstone written
	blocked, err := checker(db)(context.Background(), fixtureMaster)
	if err != nil || !blocked {
		t.Fatalf("tombstone missing (blocked=%v err=%v)", blocked, err)
	}
	// ack shape + /contract validation
	if res.Ack.Type != ErasedType || res.Ack.Source != "driver" {
		t.Fatalf("ack type/source = %q/%q", res.Ack.Type, res.Ack.Source)
	}
	if res.Ack.CausationID == nil || *res.Ack.CausationID != fixtureRequest {
		t.Fatalf("ack causationId must be the request envelope id")
	}
	validateAck(t, res.Ack)
}

// AC2: a redelivered erasure is idempotent — the inbox dedupes it, delete does not run
// twice, and it reports Duplicate.
func TestHandle_IdempotentOnRedelivery(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`INSERT INTO things (master_id, payload) VALUES (?, 'pii')`, fixtureMaster); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := newHandler(db)
	in := incomingRequest(t, fixtureRequest, fixtureRequest, fixtureMaster)

	if _, err := h.Handle(context.Background(), in); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	deletes := 0
	h.Delete = func(ctx context.Context, tx *sql.Tx, masterID string) error { deletes++; return deleteThing(ctx, tx, masterID) }
	res, err := h.Handle(context.Background(), in)
	if err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if !res.Duplicate {
		t.Fatal("redelivery must report Duplicate")
	}
	if deletes != 0 {
		t.Fatalf("delete ran %d times on a duplicate; want 0", deletes)
	}
}

// AC2 (tombstone guard): GuardedCreate skips creation when the id is tombstoned so a
// replayed event cannot resurrect an erased slice.
func TestGuardedCreate_BlocksWhenTombstoned(t *testing.T) {
	db := openDB(t)
	chk := checker(db)

	created := 0
	blocked, err := GuardedCreate(context.Background(), chk, fixtureMaster, func() error { created++; return nil })
	if err != nil || blocked {
		t.Fatalf("no tombstone yet: blocked=%v err=%v", blocked, err)
	}
	if created != 1 {
		t.Fatalf("create should have run once, ran %d", created)
	}
	// tombstone it, then guard must block.
	if _, err := db.Exec(`INSERT INTO tombstones (master_id) VALUES (?)`, fixtureMaster); err != nil {
		t.Fatal(err)
	}
	created = 0
	blocked, err = GuardedCreate(context.Background(), chk, fixtureMaster, func() error { created++; return nil })
	if err != nil {
		t.Fatalf("GuardedCreate: %v", err)
	}
	if !blocked {
		t.Fatal("a tombstoned id must block (re)creation")
	}
	if created != 0 {
		t.Fatalf("create ran %d times despite tombstone; want 0", created)
	}
}

func validateAck(t *testing.T, env messaging.Envelope) {
	t.Helper()
	dir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("ResolveContractDir: %v", err)
	}
	v, err := messaging.NewValidator(dir)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.ValidateEnvelopeBytes(b); err != nil {
		t.Fatalf("privacy.erased ack failed /contract validation: %v", err)
	}
}
