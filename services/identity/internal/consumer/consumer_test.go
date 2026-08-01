package consumer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/messaging"
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

type fakeResolver struct {
	called bool
	result consumer.ResolveResult
	err    error
	gotReq domain.LookupRequest
}

func (f *fakeResolver) Resolve(_ context.Context, env messaging.IncomingEnvelope, data domain.LookupData) (consumer.ResolveResult, error) {
	f.called = true
	f.gotReq = domain.LookupRequest{RequestID: data.RequestID, Email: data.Email, EnvelopeID: env.ID, CorrelationID: env.CorrelationID}
	return f.result, f.err
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func lookupBody(t *testing.T, id, email string) []byte {
	t.Helper()
	env := map[string]any{
		"id": id, "type": "identity.lookup_requested", "source": "frontend",
		"schemaVersion": 1, "envelopeVersion": 1,
		"occurredAt": "2026-06-15T09:14:02.117Z", "correlationId": "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		"causationId": nil,
		"data":        map[string]any{"requestId": "aa11bb22-cc33-4dd4-8ee5-ff6677889900", "email": email},
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

func newHandler(res consumer.Resolver, notify func()) (*consumer.Handler, *[]string) {
	var parks []string
	h := &consumer.Handler{
		Validate:  stubValidate,
		Resolver:  res,
		Log:       quietLog(),
		Notify:    notify,
		LookupKey: messaging.LookupRequestedRoutingKey,
		Policy:    domain.DLQPolicy{MaxAttempts: 5, BaseMs: 1000, Multiplier: 2, MaxMs: 60000},
		Retry:     func(_ context.Context, _ []byte, _, _ int) error { return nil },
		Park:      func(_ context.Context, _ []byte, reason string) error { parks = append(parks, reason); return nil },
	}
	return h, &parks
}

// --- tests -----------------------------------------------------------------

func TestProcess_MintAcksAndNotifies(t *testing.T) {
	notified := false
	res := &fakeResolver{result: consumer.ResolveResult{MasterID: "id-A", Minted: true}}
	h, parks := newHandler(res, func() { notified = true })

	d := &fakeDelivery{body: lookupBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "jeremy@example.com")}
	h.Process(context.Background(), d)

	if !res.called {
		t.Fatal("resolver was not called for a valid lookup")
	}
	if !d.acked {
		t.Fatal("delivery not acked after a successful resolve")
	}
	if !notified {
		t.Fatal("relay not kicked (Notify) after enqueueing a fresh reply")
	}
	if len(*parks) != 0 {
		t.Fatalf("unexpected parks: %v", *parks)
	}
	if res.gotReq.EnvelopeID != "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f" || res.gotReq.CorrelationID == "" {
		t.Fatalf("resolver got wrong request context: %+v", res.gotReq)
	}
}

// The email natural key is canonicalized (trim + lowercase, Q&A Round 31) BEFORE it
// reaches the resolver, so case/whitespace variants de-dup to one masterId (AC2).
func TestProcess_NormalizesEmailBeforeResolving(t *testing.T) {
	res := &fakeResolver{result: consumer.ResolveResult{MasterID: "id-A", Minted: true}}
	h, parks := newHandler(res, func() {})

	d := &fakeDelivery{body: lookupBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "  Jeremy@Example.COM  ")}
	h.Process(context.Background(), d)

	if !res.called {
		t.Fatal("resolver was not called for a valid lookup")
	}
	if res.gotReq.Email != "jeremy@example.com" {
		t.Fatalf("resolver got email %q; want the normalized form jeremy@example.com", res.gotReq.Email)
	}
	if len(*parks) != 0 {
		t.Fatalf("unexpected parks: %v", *parks)
	}
}

// A whitespace-only email normalizes to "" and is parked as a blank natural key
// (never resolved on an empty key — AC3 "never silently dropped, never resolved").
func TestProcess_WhitespaceOnlyEmailParksAsBlank(t *testing.T) {
	res := &fakeResolver{}
	h, parks := newHandler(res, func() {})
	d := &fakeDelivery{body: lookupBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "   ")}
	h.Process(context.Background(), d)
	if res.called {
		t.Fatal("a whitespace-only email must not resolve (blank natural key)")
	}
	if len(*parks) != 1 || (*parks)[0] != "blank-email" {
		t.Fatalf("whitespace-only email should park as blank-email; parks=%v", *parks)
	}
}

// AC2 (Round 33/Q33.1): a Held result (email suppressed by a prior erasure) acks the
// delivery but must NOT kick the relay (no identity.resolved reply was enqueued).
func TestProcess_HeldAcksWithoutNotify(t *testing.T) {
	notified := false
	res := &fakeResolver{result: consumer.ResolveResult{Held: true}}
	h, parks := newHandler(res, func() { notified = true })

	d := &fakeDelivery{body: lookupBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "erased@example.com")}
	h.Process(context.Background(), d)

	if !d.acked {
		t.Fatal("a held lookup must still be acked (never dropped)")
	}
	if notified {
		t.Fatal("a held lookup must NOT kick the relay (no identity.resolved was enqueued)")
	}
	if len(*parks) != 0 {
		t.Fatalf("a held lookup is not a DLQ concern; unexpected parks: %v", *parks)
	}
}

func TestProcess_DuplicateAcksWithoutNotify(t *testing.T) {
	notified := false
	res := &fakeResolver{result: consumer.ResolveResult{MasterID: "id-A", Duplicate: true}}
	h, _ := newHandler(res, func() { notified = true })

	d := &fakeDelivery{body: lookupBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "jeremy@example.com")}
	h.Process(context.Background(), d)

	if !d.acked {
		t.Fatal("a duplicate must still be acked (idempotent no-op)")
	}
	if notified {
		t.Fatal("a duplicate must NOT re-kick the relay (no second reply)")
	}
}

func TestProcess_InvalidContractParksWithoutResolving(t *testing.T) {
	res := &fakeResolver{}
	h, parks := newHandler(res, func() {})
	d := &fakeDelivery{body: lookupBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "BAD")}
	h.Process(context.Background(), d)

	if res.called {
		t.Fatal("resolver must NOT be called for a contract-invalid message")
	}
	if len(*parks) != 1 {
		t.Fatalf("invalid message should be parked once; parks=%v", *parks)
	}
}

func TestProcess_UndecodableEnvelopeParks(t *testing.T) {
	res := &fakeResolver{}
	h, parks := newHandler(res, func() {})
	d := &fakeDelivery{body: []byte("{not json")}
	h.Process(context.Background(), d)
	if res.called || len(*parks) != 1 {
		t.Fatalf("undecodable envelope should park without resolving; called=%v parks=%v", res.called, *parks)
	}
}

func TestProcess_UnhandledTypeAcksAndIgnores(t *testing.T) {
	notified := false
	res := &fakeResolver{}
	h, parks := newHandler(res, func() { notified = true })
	body := []byte(`{"id":"018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f","type":"lap.recorded","source":"timing","schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-15T09:14:02.117Z","correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{}}`)
	d := &fakeDelivery{body: body}
	h.Process(context.Background(), d)
	if res.called {
		t.Fatal("resolver must not be called for a non-lookup type")
	}
	if !d.acked || notified || len(*parks) != 0 {
		t.Fatalf("unhandled valid type should be acked + ignored (tolerant reader); acked=%v notified=%v parks=%v", d.acked, notified, *parks)
	}
}

func TestProcess_ProcessingFailureRetriesBelowCap(t *testing.T) {
	var retried bool
	res := &fakeResolver{err: errors.New("db boom")}
	h, parks := newHandler(res, func() {})
	h.Retry = func(_ context.Context, _ []byte, delayMs, next int) error {
		retried = true
		if delayMs != 1000 || next != 1 {
			t.Fatalf("first retry delay/next = %d/%d; want 1000/1", delayMs, next)
		}
		return nil
	}
	d := &fakeDelivery{body: lookupBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "jeremy@example.com"), retries: 0}
	h.Process(context.Background(), d)
	if !retried || !d.acked || len(*parks) != 0 {
		t.Fatalf("a processing failure below cap should retry + ack; retried=%v acked=%v parks=%v", retried, d.acked, *parks)
	}
}

func TestProcess_ProcessingFailureParksAtCap(t *testing.T) {
	res := &fakeResolver{err: errors.New("db boom")}
	h, parks := newHandler(res, func() {})
	d := &fakeDelivery{body: lookupBody(t, "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f", "jeremy@example.com"), retries: 4} // attempt 5 == cap
	h.Process(context.Background(), d)
	if len(*parks) != 1 || (*parks)[0] != "retries-exhausted" {
		t.Fatalf("at the cap the message should park as retries-exhausted; parks=%v", *parks)
	}
}
