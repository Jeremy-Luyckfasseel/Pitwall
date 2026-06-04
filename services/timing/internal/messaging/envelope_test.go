package messaging

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"
)

var (
	wireTimeRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	lcUUIDRE   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func TestFormatWireTime_ExactContractFormat(t *testing.T) {
	// A non-UTC input with sub-millisecond precision must normalize to UTC,
	// exactly 3 fractional digits, and a literal Z.
	loc := time.FixedZone("CEST", 2*60*60)
	in := time.Date(2026, 5, 31, 15, 45, 0, 123456789, loc) // 13:45:00.123 UTC
	got := FormatWireTime(in)
	if got != "2026-05-31T13:45:00.123Z" {
		t.Fatalf("FormatWireTime = %q, want 2026-05-31T13:45:00.123Z", got)
	}
	if !wireTimeRE.MatchString(got) {
		t.Errorf("%q does not match the contract timestamp pattern", got)
	}
}

func TestNewHeartbeatEnvelope_ShapeAndWireRules(t *testing.T) {
	now := time.Date(2026, 5, 31, 13, 45, 0, 0, time.UTC)
	env := NewHeartbeatEnvelope("timing", "inst-1", "7c9e6a55-0e42-4f8b-bd1a-3c2d1e0f9a8b", now)

	if env.Type != "control.heartbeat" {
		t.Errorf("Type = %q, want control.heartbeat", env.Type)
	}
	if env.Source != "timing" {
		t.Errorf("Source = %q, want timing", env.Source)
	}
	if env.SchemaVersion != 1 || env.EnvelopeVersion != 1 {
		t.Errorf("versions = %d/%d, want 1/1", env.SchemaVersion, env.EnvelopeVersion)
	}
	if env.CausationID != nil {
		t.Errorf("CausationID should be nil (flow-originating), got %v", *env.CausationID)
	}
	if !lcUUIDRE.MatchString(env.ID) {
		t.Errorf("envelope id %q is not a lowercase canonical UUID", env.ID)
	}
	if !wireTimeRE.MatchString(env.OccurredAt) {
		t.Errorf("occurredAt %q does not match the contract pattern", env.OccurredAt)
	}
	data, ok := env.Data.(HeartbeatData)
	if !ok {
		t.Fatalf("Data is not HeartbeatData: %T", env.Data)
	}
	if data.Service != "timing" || data.InstanceID != "inst-1" {
		t.Errorf("data = %+v, want service=timing instanceId=inst-1", data)
	}
	if !wireTimeRE.MatchString(data.At) {
		t.Errorf("data.at %q does not match the contract pattern", data.At)
	}
}

// causationId must SERIALIZE as null (present, not omitted) for a flow-originating event.
func TestHeartbeatEnvelope_CausationIdSerializesAsNull(t *testing.T) {
	env := NewHeartbeatEnvelope("timing", "inst-1", "cid", time.Now())
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	v, present := raw["causationId"]
	if !present {
		t.Fatal("causationId key must be present in the wire JSON")
	}
	if string(v) != "null" {
		t.Errorf("causationId = %s, want null", v)
	}
}
