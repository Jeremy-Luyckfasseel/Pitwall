package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func testValidator(t *testing.T) *Validator {
	t.Helper()
	dir, err := ResolveContractDir("")
	if err != nil {
		t.Fatalf("ResolveContractDir: %v", err)
	}
	v, err := NewValidator(dir)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

// The real, committed lap.recorded example validates as an outbox event — the
// relay's validate-on-publish accepts a contract-valid domain event (AC2).
func TestValidateEnvelopeBytes_AcceptsRealLapRecorded(t *testing.T) {
	v := testValidator(t)
	dir, _ := ResolveContractDir("")
	payload, err := os.ReadFile(filepath.Join(dir, "examples", "timing", "lap.recorded.v1.example.json"))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	if err := v.ValidateEnvelopeBytes(payload); err != nil {
		t.Fatalf("valid lap.recorded rejected: %v", err)
	}
}

// A structurally broken envelope (correlationId not a UUID) is rejected — this
// is the row the relay marks dead, never publishes (AC2).
func TestValidateEnvelopeBytes_RejectsBadEnvelope(t *testing.T) {
	v := testValidator(t)
	bad := mutateExample(t, func(m map[string]any) { m["correlationId"] = "not-a-uuid" })
	if err := v.ValidateEnvelopeBytes(bad); err == nil {
		t.Fatalf("expected rejection for bad correlationId, got nil")
	}
}

// Invalid data (lapTimeMs below the schema minimum) is rejected even though the
// envelope itself is well-formed — two-sided validation, data half.
func TestValidateEnvelopeBytes_RejectsBadData(t *testing.T) {
	v := testValidator(t)
	bad := mutateExample(t, func(m map[string]any) {
		data := m["data"].(map[string]any)
		data["lapTimeMs"] = 0 // schema requires minimum 1
	})
	if err := v.ValidateEnvelopeBytes(bad); err == nil {
		t.Fatalf("expected rejection for lapTimeMs=0, got nil")
	}
}

// An event whose type has no /contract schema fails closed: we never publish an
// event we cannot validate.
func TestValidateEnvelopeBytes_RejectsUnknownType(t *testing.T) {
	v := testValidator(t)
	bad := mutateExample(t, func(m map[string]any) { m["type"] = "unknown.event" })
	if err := v.ValidateEnvelopeBytes(bad); err == nil {
		t.Fatalf("expected rejection for unknown type, got nil")
	}
}

// mutateExample loads the real lap.recorded example, applies fn, and re-marshals.
func mutateExample(t *testing.T, fn func(map[string]any)) []byte {
	t.Helper()
	dir, _ := ResolveContractDir("")
	raw, err := os.ReadFile(filepath.Join(dir, "examples", "timing", "lap.recorded.v1.example.json"))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal example: %v", err)
	}
	fn(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal mutated: %v", err)
	}
	return out
}
