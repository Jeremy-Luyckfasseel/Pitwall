package consumer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/dlq"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/consumer"
)

// --- fakes -----------------------------------------------------------------

type fakeDelivery struct {
	body    []byte
	retries int
	acked   bool
	nacked  bool
	requeue bool
}

func (f *fakeDelivery) Body() []byte { return f.body }
func (f *fakeDelivery) Ack() error   { f.acked = true; return nil }
func (f *fakeDelivery) Nack(requeue bool) error {
	f.nacked = true
	f.requeue = requeue
	return nil
}
func (f *fakeDelivery) RetryCount() int { return f.retries }

type fakeDeliverer struct {
	calls     int
	duplicate bool
	err       error
	got       consumer.Resolved
}

func (f *fakeDeliverer) Deliver(_ context.Context, envID, envType, processedAt string, r consumer.Resolved) (bool, error) {
	f.calls++
	f.got = r
	return f.duplicate, f.err
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var pol = dlq.Policy{MaxAttempts: 5, BaseMs: 1000, Multiplier: 2, MaxMs: 60000}

func resolvedBody(t *testing.T, id, requestID, masterID string) []byte {
	t.Helper()
	env := map[string]any{
		"id": id, "type": "identity.resolved", "source": "identity",
		"schemaVersion": 1, "envelopeVersion": 1,
		"occurredAt": "2026-06-15T09:14:02.117Z", "correlationId": "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		"causationId": "cc33dd44-ee55-4ff6-8a77-b88c99d00e11",
		"data":        map[string]any{"requestId": requestID, "email": "sim-driver-1@pitwall.test", "masterId": masterID},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// stubValidate treats any body containing "BAD" as contract-invalid.
func stubValidate(body []byte) error {
	if bytes.Contains(body, []byte("BAD")) {
		return errors.New("stub: contract-invalid")
	}
	return nil
}

func newHandler(del consumer.Deliverer) (*consumer.Handler, *[]string) {
	parked := &[]string{}
	h := &consumer.Handler{
		Validate:    stubValidate,
		Deliverer:   del,
		Log:         quietLog(),
		ResolvedKey: "identity.resolved",
		Policy:      pol,
		Retry:       func(_ context.Context, _ []byte, _, _ int) error { return nil },
		Park: func(_ context.Context, _ []byte, reason string) error {
			*parked = append(*parked, reason)
			return nil
		},
	}
	return h, parked
}

// --- tests -----------------------------------------------------------------

// Happy path: a valid identity.resolved is delivered to the resolver, then acked.
func TestProcess_DeliversAndAcks(t *testing.T) {
	del := &fakeDeliverer{}
	h, parked := newHandler(del)
	d := &fakeDelivery{body: resolvedBody(t, "018f7a2c-9d3e-7c41-b8a2-1e6f4d2c5b09",
		"aa11bb22-cc33-4dd4-8ee5-ff6677889900", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")}
	h.Process(context.Background(), d)

	if del.calls != 1 {
		t.Fatalf("Deliver calls = %d, want 1", del.calls)
	}
	if del.got.MasterID != "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21" || del.got.RequestID != "aa11bb22-cc33-4dd4-8ee5-ff6677889900" {
		t.Errorf("delivered wrong payload: %+v", del.got)
	}
	if !d.acked {
		t.Errorf("delivery not acked")
	}
	if len(*parked) != 0 {
		t.Errorf("nothing should be parked, got %v", *parked)
	}
}

// A redelivered envelope id is a no-op: the deliverer reports Duplicate, no panic,
// still acked (the simulator already got its id; never a second check-in).
func TestProcess_DuplicateIsAckedNoop(t *testing.T) {
	del := &fakeDeliverer{duplicate: true}
	h, _ := newHandler(del)
	d := &fakeDelivery{body: resolvedBody(t, "id-dup", "aa11bb22-cc33-4dd4-8ee5-ff6677889900", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")}
	h.Process(context.Background(), d)
	if !d.acked {
		t.Errorf("a duplicate must still be acked")
	}
}

// A contract-invalid message parks immediately (never delivered, never retried as poison).
func TestProcess_InvalidParks(t *testing.T) {
	del := &fakeDeliverer{}
	h, parked := newHandler(del)
	d := &fakeDelivery{body: []byte(`{"type":"identity.resolved","data":"BAD"}`)}
	h.Process(context.Background(), d)
	if del.calls != 0 {
		t.Errorf("invalid message must NOT be delivered")
	}
	if len(*parked) != 1 || (*parked)[0] != "contract-invalid" {
		t.Errorf("expected a contract-invalid park, got %v", *parked)
	}
	if !d.acked {
		t.Errorf("a parked message acks the original after the park publish")
	}
}

// A valid but unhandled type is acked + ignored (tolerant reader).
func TestProcess_UnhandledTypeIgnored(t *testing.T) {
	del := &fakeDeliverer{}
	h, parked := newHandler(del)
	body := resolvedBody(t, "id-x", "aa11bb22-cc33-4dd4-8ee5-ff6677889900", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")
	body = bytes.Replace(body, []byte(`"type":"identity.resolved"`), []byte(`"type":"some.other.event"`), 1)
	h.Process(context.Background(), &fakeDelivery{body: body})
	if del.calls != 0 {
		t.Errorf("unhandled type must not be delivered")
	}
	if len(*parked) != 0 {
		t.Errorf("unhandled type is ignored, not parked: %v", *parked)
	}
}

// A processing (Deliver) failure below the cap schedules a retry, then acks.
func TestProcess_ProcessingFailureRetries(t *testing.T) {
	del := &fakeDeliverer{err: errors.New("inbox db boom")}
	h, parked := newHandler(del)
	var retried bool
	h.Retry = func(_ context.Context, _ []byte, _, _ int) error { retried = true; return nil }
	d := &fakeDelivery{retries: 0, body: resolvedBody(t, "id-r", "aa11bb22-cc33-4dd4-8ee5-ff6677889900", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")}
	h.Process(context.Background(), d)
	if !retried {
		t.Errorf("a processing failure below the cap must schedule a retry")
	}
	if len(*parked) != 0 {
		t.Errorf("should retry, not park, below the cap")
	}
	if !d.acked {
		t.Errorf("after scheduling a retry the original is acked")
	}
}

// At the retry cap a processing failure parks.
func TestProcess_ProcessingFailureParksAtCap(t *testing.T) {
	del := &fakeDeliverer{err: errors.New("inbox db boom")}
	h, parked := newHandler(del)
	d := &fakeDelivery{retries: 4, body: resolvedBody(t, "id-cap", "aa11bb22-cc33-4dd4-8ee5-ff6677889900", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")}
	h.Process(context.Background(), d)
	if len(*parked) != 1 || (*parked)[0] != "retries-exhausted" {
		t.Errorf("expected a retries-exhausted park at the cap, got %v", *parked)
	}
}
