package messaging

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewCheckedInEnvelope_QR_Validates(t *testing.T) {
	v := testValidator(t)
	at := time.Date(2026, 6, 5, 13, 58, 2, 140_000_000, time.UTC)
	env := NewCheckedInEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		fixtureMasterID, "qr", nil, at)

	if env.Type != DriverCheckedInRoutingKey {
		t.Errorf("Type = %q, want %q", env.Type, DriverCheckedInRoutingKey)
	}
	if env.Source != "timing" {
		t.Errorf("Source = %q, want timing", env.Source)
	}
	if env.OccurredAt != "2026-06-05T13:58:02.140Z" {
		t.Errorf("OccurredAt = %q, want pinned wire format", env.OccurredAt)
	}
	d, ok := env.Data.(CheckedInData)
	if !ok {
		t.Fatalf("Data is %T, want CheckedInData", env.Data)
	}
	if d.At != env.OccurredAt {
		t.Errorf("data.at (%q) should equal occurredAt (%q)", d.At, env.OccurredAt)
	}
	if d.CheckInMethod != "qr" {
		t.Errorf("checkInMethod = %q, want qr", d.CheckInMethod)
	}
	if d.TransponderID != nil {
		t.Errorf("transponderId should be nil for a QR driver, got %v", *d.TransponderID)
	}
	if err := v.ValidateEnvelopeBytes(mustMarshal(t, env)); err != nil {
		t.Fatalf("generated driver.checked_in (qr) rejected by /contract: %v", err)
	}

	// transponderId must serialize as JSON null (present, never omitted) for QR.
	var m map[string]any
	if err := json.Unmarshal(mustMarshal(t, env), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data := m["data"].(map[string]any)
	if val, present := data["transponderId"]; !present || val != nil {
		t.Errorf("transponderId should be present and null for QR; present=%v value=%v", present, val)
	}
}

func TestNewCheckedInEnvelope_Transponder_Validates(t *testing.T) {
	v := testValidator(t)
	at := time.Date(2026, 6, 5, 13, 58, 2, 140_000_000, time.UTC)
	tp := "TP-00421"
	env := NewCheckedInEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		fixtureMasterID, "transponder", &tp, at)

	d, ok := env.Data.(CheckedInData)
	if !ok {
		t.Fatalf("Data is %T, want CheckedInData", env.Data)
	}
	if d.CheckInMethod != "transponder" {
		t.Errorf("checkInMethod = %q, want transponder", d.CheckInMethod)
	}
	if d.TransponderID == nil || *d.TransponderID != "TP-00421" {
		t.Errorf("transponderId = %v, want TP-00421", d.TransponderID)
	}
	if err := v.ValidateEnvelopeBytes(mustMarshal(t, env)); err != nil {
		t.Fatalf("generated driver.checked_in (transponder) rejected by /contract: %v", err)
	}
}
