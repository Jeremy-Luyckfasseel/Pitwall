package messaging

import (
	"testing"
	"time"
)

func newTestValidator(t *testing.T) *Validator {
	t.Helper()
	dir, err := ResolveContractDir("")
	if err != nil {
		t.Fatalf("could not locate /contract: %v", err)
	}
	v, err := NewValidator(dir)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

func TestValidateHeartbeat_AcceptsAWellFormedHeartbeat(t *testing.T) {
	v := newTestValidator(t)
	env := NewHeartbeatEnvelope("svc", "b3d4e5f6-1a2b-4c3d-8e9f-0a1b2c3d4e5f",
		"7c9e6a55-0e42-4f8b-bd1a-3c2d1e0f9a8b", time.Date(2026, 5, 31, 13, 45, 0, 0, time.UTC))
	if err := v.ValidateHeartbeat(env); err != nil {
		t.Fatalf("a well-formed heartbeat must validate, got: %v", err)
	}
}

func TestValidateHeartbeat_RejectsBadTimestamp(t *testing.T) {
	v := newTestValidator(t)
	env := NewHeartbeatEnvelope("svc", "inst", "7c9e6a55-0e42-4f8b-bd1a-3c2d1e0f9a8b", time.Now())
	// Corrupt data.at to a non-canonical offset form (the 1.2 gotcha: format is
	// annotation-only, so this must be caught by the pinned pattern).
	d := env.Data.(HeartbeatData)
	d.At = "2026-05-31T13:45:00+00:00"
	env.Data = d
	if err := v.ValidateHeartbeat(env); err == nil {
		t.Fatal("expected rejection for a non-canonical data.at timestamp")
	}
}

func TestValidateHeartbeat_RejectsMissingRequiredField(t *testing.T) {
	v := newTestValidator(t)
	env := NewHeartbeatEnvelope("svc", "inst", "7c9e6a55-0e42-4f8b-bd1a-3c2d1e0f9a8b", time.Now())
	// Replace data with a map lacking instanceId to force the missing-required case.
	env.Data = map[string]any{"service": "svc", "at": FormatWireTime(time.Now())}
	if err := v.ValidateHeartbeat(env); err == nil {
		t.Fatal("expected rejection when required data.instanceId is absent")
	}
}

func TestValidateHeartbeat_RejectsBadEnvelopeType(t *testing.T) {
	v := newTestValidator(t)
	env := NewHeartbeatEnvelope("svc", "inst", "7c9e6a55-0e42-4f8b-bd1a-3c2d1e0f9a8b", time.Now())
	env.Type = "heartbeat" // bare, no dot -> fails the envelope `type` pattern
	if err := v.ValidateHeartbeat(env); err == nil {
		t.Fatal("expected rejection for a bare 'heartbeat' type (envelope pattern requires entity.action)")
	}
}
