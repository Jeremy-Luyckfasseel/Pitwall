package consumer_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
)

func newResolver(t *testing.T, publish func(context.Context, messaging.Envelope) error) *consumer.Resolver {
	t.Helper()
	db, err := persistence.Open(context.Background(), filepath.Join(t.TempDir(), "timing.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &consumer.Resolver{
		DB:      db,
		Publish: publish,
		Source:  "frontend",
		Log:     quietLog(),
	}
}

// Resolve publishes an identity.lookup_requested for the email, then blocks until the
// matching identity.resolved is delivered — returning the resolved masterId (the
// register-first request/reply over the bus).
func TestResolver_ResolveReturnsMasterIDOnDeliver(t *testing.T) {
	published := make(chan messaging.Envelope, 1)
	r := newResolver(t, func(_ context.Context, env messaging.Envelope) error {
		published <- env
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	type result struct {
		id  string
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := r.Resolve(ctx, "sim-driver-1@pitwall.test")
		done <- result{id, err}
	}()

	env := <-published
	if env.Type != messaging.LookupRequestedRoutingKey {
		t.Fatalf("published type = %q, want identity.lookup_requested", env.Type)
	}
	if env.Source != "frontend" {
		t.Errorf("lookup source = %q, want frontend (the registration stand-in)", env.Source)
	}
	data, ok := env.Data.(messaging.LookupRequestedData)
	if !ok {
		t.Fatalf("lookup data is %T, want LookupRequestedData", env.Data)
	}
	if data.Email != "sim-driver-1@pitwall.test" {
		t.Errorf("lookup email = %q", data.Email)
	}

	// Identity replies: deliver the resolved id correlated by requestId.
	dup, err := r.Deliver(ctx, "env-1", "identity.resolved", "2026-06-15T09:14:02.117Z",
		consumer.Resolved{RequestID: data.RequestID, Email: data.Email, MasterID: "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"})
	if err != nil || dup {
		t.Fatalf("Deliver: dup=%v err=%v", dup, err)
	}

	res := <-done
	if res.err != nil {
		t.Fatalf("Resolve returned error: %v", res.err)
	}
	if res.id != "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21" {
		t.Errorf("Resolve = %q, want the delivered masterId", res.id)
	}
}

// Resolve re-publishes the lookup if no reply arrives within RetryInterval — so a
// lookup lost before Identity bound its queue (startup race), or Identity restarting,
// never hangs a driver forever. The requestId is stable across retries (correlation).
func TestResolver_RetriesPublishUntilDelivered(t *testing.T) {
	var mu sync.Mutex
	var pubs []messaging.Envelope
	r := newResolver(t, func(_ context.Context, env messaging.Envelope) error {
		mu.Lock()
		pubs = append(pubs, env)
		mu.Unlock()
		return nil
	})
	r.RetryInterval = 20 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan string, 1)
	go func() {
		id, _ := r.Resolve(ctx, "x@pitwall.test")
		done <- id
	}()

	// Wait until the lookup has been re-published at least twice (proves the retry).
	deadline := time.Now().Add(1 * time.Second)
	for {
		mu.Lock()
		n := len(pubs)
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected >=2 lookup publishes (retry), saw %d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	reqID := pubs[0].Data.(messaging.LookupRequestedData).RequestID
	mu.Unlock()
	for i := range pubs {
		if pubs[i].Data.(messaging.LookupRequestedData).RequestID != reqID {
			t.Fatalf("requestId must be stable across retries")
		}
	}

	if _, err := r.Deliver(ctx, "env-retry", "identity.resolved", "2026-06-15T09:14:02.117Z",
		consumer.Resolved{RequestID: reqID, Email: "x@pitwall.test", MasterID: "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := <-done; got != "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21" {
		t.Errorf("Resolve = %q, want the delivered masterId", got)
	}
}

// A PERMANENT publish failure (e.g. a contract-invalid lookup) must fail fast — Resolve
// returns the error immediately instead of re-publishing an un-publishable lookup forever.
func TestResolver_PermanentPublishErrorFailsFast(t *testing.T) {
	boom := errors.New("contract-invalid lookup")
	r := newResolver(t, func(context.Context, messaging.Envelope) error {
		return consumer.Permanent(boom)
	})
	r.RetryInterval = time.Hour // would hang for an hour if it retried
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.Resolve(ctx, "x@pitwall.test")
	if err == nil {
		t.Fatal("expected Resolve to fail fast on a permanent publish error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error should unwrap to the cause; got %v", err)
	}
}

// A redelivered identity.resolved (same envelope id) is deduped by the inbox -> the
// second Deliver reports duplicate, so the simulator is never signalled twice.
func TestResolver_DeliverDuplicateNoop(t *testing.T) {
	r := newResolver(t, func(context.Context, messaging.Envelope) error { return nil })
	ctx := context.Background()
	res := consumer.Resolved{RequestID: "aa11bb22-cc33-4dd4-8ee5-ff6677889900", Email: "x@pitwall.test", MasterID: "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"}

	if dup, err := r.Deliver(ctx, "env-dup", "identity.resolved", "2026-06-15T09:14:02.117Z", res); err != nil || dup {
		t.Fatalf("first Deliver: dup=%v err=%v", dup, err)
	}
	dup, err := r.Deliver(ctx, "env-dup", "identity.resolved", "2026-06-15T09:14:02.117Z", res)
	if err != nil {
		t.Fatalf("second Deliver err: %v", err)
	}
	if !dup {
		t.Errorf("redelivered same envelope id should be a duplicate no-op")
	}
}

// A reply with no live waiter (late / unknown requestId) is graceful: not a duplicate,
// no error, no panic (the requester already moved on, or it is a stray replay).
func TestResolver_DeliverNoWaiterIsGraceful(t *testing.T) {
	r := newResolver(t, func(context.Context, messaging.Envelope) error { return nil })
	dup, err := r.Deliver(context.Background(), "env-orphan", "identity.resolved", "2026-06-15T09:14:02.117Z",
		consumer.Resolved{RequestID: "no-one-waits", Email: "x@pitwall.test", MasterID: "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"})
	if err != nil || dup {
		t.Errorf("orphan delivery should be graceful (dup=false,err=nil); got dup=%v err=%v", dup, err)
	}
}
