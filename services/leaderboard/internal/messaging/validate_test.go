package messaging

import (
	"os"
	"path/filepath"
	"testing"
)

// testValidator compiles the real /contract tree (resolved by walking up from the
// test's working dir). This is the same tree COPYed into the image at runtime.
func testValidator(t *testing.T) (*Validator, string) {
	t.Helper()
	dir, err := ResolveContractDir("")
	if err != nil {
		t.Fatalf("resolve /contract: %v", err)
	}
	v, err := NewValidator(dir)
	if err != nil {
		t.Fatalf("compile schemas: %v", err)
	}
	return v, dir
}

func readFixture(t *testing.T, contractDir, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(contractDir, rel))
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	return b
}

// AC1 (validate-on-consume): the committed VALID lap.recorded example passes.
func TestValidateEnvelopeBytes_AcceptsCommittedLapRecordedExample(t *testing.T) {
	v, dir := testValidator(t)
	payload := readFixture(t, dir, "examples/timing/lap.recorded.v1.example.json")
	if err := v.ValidateEnvelopeBytes(payload); err != nil {
		t.Errorf("valid lap.recorded example should pass validation, got: %v", err)
	}
}

// AC1 (validate-on-consume): the committed KNOWN-BAD fixture is rejected — an
// invalid message must never be applied to the read-model (logged + rejected).
func TestValidateEnvelopeBytes_RejectsCommittedInvalidFixture(t *testing.T) {
	v, dir := testValidator(t)
	payload := readFixture(t, dir, "examples/timing/lap.recorded.v1.invalid.json")
	if err := v.ValidateEnvelopeBytes(payload); err == nil {
		t.Error("known-bad lap.recorded fixture must be rejected on consume, got nil error")
	}
}

// Fail-closed: an envelope whose type has no registered /contract data schema is
// rejected (the consumer must never apply an event it cannot validate).
func TestValidateEnvelopeBytes_RejectsUnknownType(t *testing.T) {
	v, _ := testValidator(t)
	payload := []byte(`{"id":"018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f","type":"nonsense.event","source":"x","schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-08T10:00:00.000Z","correlationId":"018f9e2a-7c3d-7b21-9c4e-000000000001","causationId":null,"data":{}}`)
	if err := v.ValidateEnvelopeBytes(payload); err == nil {
		t.Error("an unknown event type must fail closed, got nil error")
	}
}
