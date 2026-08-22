package messaging

import (
	"encoding/json"
	"testing"
	"time"
)

// A fixture masterId that satisfies the lap schema's UUID-v4 pattern.
const fixtureMasterID = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"

// testValidator builds a /contract validator for the service's validate-on-publish
// domain tests (the generic validator mechanics are tested in libs/go-pitwall).
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

func mustMarshal(t *testing.T, env Envelope) []byte {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

func TestNewLapRecordedEnvelope_Validates(t *testing.T) {
	v := testValidator(t)
	at := time.Date(2026, 6, 5, 14, 3, 21, 500_000_000, time.UTC)
	tp := "TP-00421"
	env := NewLapRecordedEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		fixtureMasterID, "sim-20260605T140000000Z", 7, 42318, &tp, at)

	if env.Type != LapRecordedRoutingKey {
		t.Errorf("Type = %q, want %q", env.Type, LapRecordedRoutingKey)
	}
	if env.Source != "timing" {
		t.Errorf("Source = %q, want timing", env.Source)
	}
	if env.OccurredAt != "2026-06-05T14:03:21.500Z" {
		t.Errorf("OccurredAt = %q, want pinned wire format", env.OccurredAt)
	}
	d, ok := env.Data.(LapRecordedData)
	if !ok {
		t.Fatalf("Data is %T, want LapRecordedData", env.Data)
	}
	if d.At != env.OccurredAt {
		t.Errorf("data.at (%q) should equal occurredAt (%q)", d.At, env.OccurredAt)
	}
	if d.LapNumber != 7 || d.LapTimeMs != 42318 {
		t.Errorf("lapNumber/lapTimeMs = %d/%d, want 7/42318", d.LapNumber, d.LapTimeMs)
	}
	if err := v.ValidateEnvelopeBytes(mustMarshal(t, env)); err != nil {
		t.Fatalf("generated lap.recorded rejected by /contract: %v", err)
	}
}

func TestNewSessionStartedEnvelope_Validates(t *testing.T) {
	v := testValidator(t)
	startedAt := time.Date(2026, 6, 5, 13, 45, 0, 0, time.UTC)
	env := NewSessionStartedEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		"sim-20260605T134500000Z", startedAt)

	if env.Type != SessionStartedRoutingKey {
		t.Errorf("Type = %q, want %q", env.Type, SessionStartedRoutingKey)
	}
	if env.OccurredAt != "2026-06-05T13:45:00.000Z" {
		t.Errorf("OccurredAt = %q, want pinned wire format", env.OccurredAt)
	}
	if err := v.ValidateEnvelopeBytes(mustMarshal(t, env)); err != nil {
		t.Fatalf("generated session.started rejected by /contract: %v", err)
	}
}

func TestNewSessionEndedEnvelope_Validates(t *testing.T) {
	v := testValidator(t)
	endedAt := time.Date(2026, 6, 5, 14, 5, 0, 0, time.UTC)
	summary := []SessionSummaryRow{{MasterID: fixtureMasterID, BestLapMs: 41980, LapCount: 12}}
	env := NewSessionEndedEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		"sim-20260605T134500000Z", endedAt, summary)

	if env.Type != SessionEndedRoutingKey {
		t.Errorf("Type = %q, want %q", env.Type, SessionEndedRoutingKey)
	}
	if env.OccurredAt != "2026-06-05T14:05:00.000Z" {
		t.Errorf("OccurredAt = %q, want pinned wire format", env.OccurredAt)
	}
	if err := v.ValidateEnvelopeBytes(mustMarshal(t, env)); err != nil {
		t.Fatalf("generated session.ended rejected by /contract: %v", err)
	}
}

func TestNewPersonalRecordBrokenEnvelope_Validates(t *testing.T) {
	v := testValidator(t)
	at := time.Date(2026, 6, 5, 14, 3, 21, 500_000_000, time.UTC)
	previous := int64(42318)
	env := NewPersonalRecordBrokenEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		fixtureMasterID, "sim-20260605T140000000Z", 41980, &previous, at)

	if env.Type != PersonalRecordBrokenRoutingKey {
		t.Errorf("Type = %q, want %q", env.Type, PersonalRecordBrokenRoutingKey)
	}
	if env.OccurredAt != "2026-06-05T14:03:21.500Z" {
		t.Errorf("OccurredAt = %q, want pinned wire format", env.OccurredAt)
	}
	d, ok := env.Data.(PersonalRecordBrokenData)
	if !ok {
		t.Fatalf("Data is %T, want PersonalRecordBrokenData", env.Data)
	}
	if d.LapTimeMs != 41980 || d.PreviousMs == nil || *d.PreviousMs != 42318 {
		t.Errorf("lapTimeMs/previousMs = %d/%v, want 41980/42318", d.LapTimeMs, d.PreviousMs)
	}
	if err := v.ValidateEnvelopeBytes(mustMarshal(t, env)); err != nil {
		t.Fatalf("generated personal_record.broken rejected by /contract: %v", err)
	}
}

// AC1 / Q37.2: a FIRST-ever PR omits previousMs entirely and still validates (previousMs
// is optional; the field must NOT serialize as null, which the schema would reject).
func TestNewPersonalRecordBrokenEnvelope_FirstPR_OmitsPreviousAndValidates(t *testing.T) {
	v := testValidator(t)
	at := time.Date(2026, 6, 5, 14, 3, 21, 500_000_000, time.UTC)
	env := NewPersonalRecordBrokenEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		fixtureMasterID, "sim-x", 41980, nil, at)

	body := mustMarshal(t, env)
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := m["data"].(map[string]any)
	if _, present := d["previousMs"]; present {
		t.Error("first-PR personal_record.broken must OMIT previousMs (schema rejects null)")
	}
	if err := v.ValidateEnvelopeBytes(body); err != nil {
		t.Fatalf("first-PR personal_record.broken rejected by /contract: %v", err)
	}
}

// Story 3.5 / AC1: scanner.offline is flow-originating and validates against /contract;
// gapFrom is carried verbatim (the last good crossing's wire time), since is stamped.
func TestNewScannerOfflineEnvelope_Validates(t *testing.T) {
	v := testValidator(t)
	since := time.Date(2026, 6, 5, 14, 12, 7, 250_000_000, time.UTC)
	env := NewScannerOfflineEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		"start-finish", "2026-06-05T14:11:52.900Z", since)

	if env.Type != ScannerOfflineRoutingKey {
		t.Errorf("Type = %q, want %q", env.Type, ScannerOfflineRoutingKey)
	}
	if env.OccurredAt != "2026-06-05T14:12:07.250Z" {
		t.Errorf("OccurredAt = %q, want pinned wire format", env.OccurredAt)
	}
	d, ok := env.Data.(ScannerOfflineData)
	if !ok {
		t.Fatalf("Data is %T, want ScannerOfflineData", env.Data)
	}
	if d.ScannerID != "start-finish" || d.Since != "2026-06-05T14:12:07.250Z" || d.GapFrom != "2026-06-05T14:11:52.900Z" {
		t.Errorf("data = %+v, want scannerId=start-finish since=…07.250Z gapFrom=…52.900Z", d)
	}
	if err := v.ValidateEnvelopeBytes(mustMarshal(t, env)); err != nil {
		t.Fatalf("generated scanner.offline rejected by /contract: %v", err)
	}
}

// Story 3.5 / AC2: scanner.online is flow-originating and validates against /contract.
func TestNewScannerOnlineEnvelope_Validates(t *testing.T) {
	v := testValidator(t)
	at := time.Date(2026, 6, 5, 14, 12, 41, 600_000_000, time.UTC)
	env := NewScannerOnlineEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		"start-finish", at)

	if env.Type != ScannerOnlineRoutingKey {
		t.Errorf("Type = %q, want %q", env.Type, ScannerOnlineRoutingKey)
	}
	if env.OccurredAt != "2026-06-05T14:12:41.600Z" {
		t.Errorf("OccurredAt = %q, want pinned wire format", env.OccurredAt)
	}
	d, ok := env.Data.(ScannerOnlineData)
	if !ok {
		t.Fatalf("Data is %T, want ScannerOnlineData", env.Data)
	}
	if d.ScannerID != "start-finish" || d.At != "2026-06-05T14:12:41.600Z" {
		t.Errorf("data = %+v, want scannerId=start-finish at=…41.600Z", d)
	}
	if err := v.ValidateEnvelopeBytes(mustMarshal(t, env)); err != nil {
		t.Fatalf("generated scanner.online rejected by /contract: %v", err)
	}
}

// CausationID must serialize as JSON null (present, never omitted) for a
// flow-originating simulator event (AR7 / wire MUST).
func TestDomainEnvelopes_CausationIDSerializesNull(t *testing.T) {
	at := time.Date(2026, 6, 5, 14, 3, 21, 500_000_000, time.UTC)
	env := NewLapRecordedEnvelope("timing", "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		fixtureMasterID, "sim-x", 1, 1000, nil, at)
	var m map[string]any
	if err := json.Unmarshal(mustMarshal(t, env), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, present := m["causationId"]; !present || v != nil {
		t.Errorf("causationId should be present and null; present=%v value=%v", present, v)
	}
	// transponderId nil must also serialize as null (no omitempty on the wire).
	d := m["data"].(map[string]any)
	if v, present := d["transponderId"]; !present || v != nil {
		t.Errorf("transponderId should be present and null when unset; present=%v value=%v", present, v)
	}
}
