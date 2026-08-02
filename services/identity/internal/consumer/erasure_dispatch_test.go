package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	goperasure "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/erasure"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/messaging"
)

type fakeEraser struct {
	called bool
	result goperasure.Result
	err    error
	gotEnv messaging.IncomingEnvelope
}

func (f *fakeEraser) Handle(_ context.Context, env messaging.IncomingEnvelope) (goperasure.Result, error) {
	f.called = true
	f.gotEnv = env
	return f.result, f.err
}

func erasureBody(t *testing.T, id, masterID string) []byte {
	t.Helper()
	env := map[string]any{
		"id": id, "type": goperasure.ErasureRequestedType, "source": "frontend",
		"schemaVersion": 1, "envelopeVersion": 1,
		"occurredAt": "2026-08-01T11:30:00.000Z", "correlationId": "e9b4f3d5-2a6c-4b8a-bf43-5d7e9a1b3c45",
		"causationId": nil,
		"data": map[string]any{
			"requestId": "e9b4f3d5-2a6c-4b8a-bf43-5d7e9a1b3c45", "masterId": masterID,
			"requestedBy": "self", "adminActor": nil, "at": "2026-08-01T11:30:00.000Z",
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newErasureHandler(eraser consumer.Eraser, notify func()) (*consumer.Handler, *[]string) {
	var parks []string
	h := &consumer.Handler{
		Validate:   func([]byte) error { return nil },
		Resolver:   &fakeResolver{},
		Erasure:    eraser,
		Log:        quietLog(),
		Notify:     notify,
		LookupKey:  messaging.LookupRequestedRoutingKey,
		ErasureKey: goperasure.ErasureRequestedType,
		Policy:     domain.DLQPolicy{MaxAttempts: 5, BaseMs: 1000, Multiplier: 2, MaxMs: 60000},
		Retry:      func(_ context.Context, _ []byte, _, _ int) error { return nil },
		Park:       func(_ context.Context, _ []byte, reason string) error { parks = append(parks, reason); return nil },
	}
	return h, &parks
}

func TestProcess_ErasureSuccessAcksAndNotifies(t *testing.T) {
	notified := false
	er := &fakeEraser{result: goperasure.Result{MasterID: "id-A", RequestID: "req-A"}}
	h, parks := newErasureHandler(er, func() { notified = true })

	d := &fakeDelivery{body: erasureBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "id-A")}
	h.Process(context.Background(), d)

	if !er.called {
		t.Fatal("Eraser.Handle was not called for a valid erasure request")
	}
	if er.gotEnv.ID != "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f" {
		t.Fatalf("Eraser got envelope id %q; want the delivered envelope", er.gotEnv.ID)
	}
	if !d.acked {
		t.Fatal("delivery not acked after a successful erasure")
	}
	if !notified {
		t.Fatal("relay not kicked (Notify) after a successful erasure enqueued its ack")
	}
	if len(*parks) != 0 {
		t.Fatalf("unexpected parks: %v", *parks)
	}
}

func TestProcess_ErasureDuplicateAcksWithoutNotify(t *testing.T) {
	notified := false
	er := &fakeEraser{result: goperasure.Result{MasterID: "id-A", Duplicate: true}}
	h, _ := newErasureHandler(er, func() { notified = true })

	d := &fakeDelivery{body: erasureBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "id-A")}
	h.Process(context.Background(), d)

	if !d.acked {
		t.Fatal("a duplicate erasure must still be acked (idempotent no-op)")
	}
	if notified {
		t.Fatal("a duplicate erasure must NOT re-kick the relay (no second ack)")
	}
}

func TestProcess_ErasureProcessingFailureRetriesBelowCap(t *testing.T) {
	var retried bool
	er := &fakeEraser{err: errors.New("db boom")}
	h, parks := newErasureHandler(er, func() {})
	h.Retry = func(_ context.Context, _ []byte, delayMs, next int) error {
		retried = true
		if delayMs != 1000 || next != 1 {
			t.Fatalf("first retry delay/next = %d/%d; want 1000/1", delayMs, next)
		}
		return nil
	}
	d := &fakeDelivery{body: erasureBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "id-A"), retries: 0}
	h.Process(context.Background(), d)
	if !retried || !d.acked || len(*parks) != 0 {
		t.Fatalf("a processing failure below cap should retry + ack; retried=%v acked=%v parks=%v", retried, d.acked, *parks)
	}
}

func TestProcess_ErasureProcessingFailureParksAtCap(t *testing.T) {
	er := &fakeEraser{err: errors.New("db boom")}
	h, parks := newErasureHandler(er, func() {})
	d := &fakeDelivery{body: erasureBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "id-A"), retries: 4} // attempt 5 == cap
	h.Process(context.Background(), d)
	if len(*parks) != 1 || (*parks)[0] != "retries-exhausted" {
		t.Fatalf("at the cap the erasure should park as retries-exhausted; parks=%v", *parks)
	}
}

// Regression guard: an unrelated valid type still falls through to the pre-existing
// tolerant-reader ack+ignore path, unaffected by the new erasure dispatch branch.
func TestProcess_UnrelatedTypeStillTolerantReaderIgnoredWithErasureWired(t *testing.T) {
	notified := false
	er := &fakeEraser{}
	h, parks := newErasureHandler(er, func() { notified = true })
	body := []byte(`{"id":"018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f","type":"lap.recorded","source":"timing","schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-15T09:14:02.117Z","correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{}}`)
	d := &fakeDelivery{body: body}
	h.Process(context.Background(), d)
	if er.called {
		t.Fatal("Eraser must not be called for a non-erasure type")
	}
	if !d.acked || notified || len(*parks) != 0 {
		t.Fatalf("unhandled valid type should be acked + ignored; acked=%v notified=%v parks=%v", d.acked, notified, *parks)
	}
}
