package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
)

// fakeStore is an in-memory stand-in for the outbox store, recording the lifecycle
// calls the relay makes.
type fakeStore struct {
	pending     []persistence.OutboxRow
	sent        []string
	quarantined []string
	failed      []string
}

func (f *fakeStore) FetchPending(_ context.Context, limit int) ([]persistence.OutboxRow, error) {
	if limit < len(f.pending) {
		return f.pending[:limit], nil
	}
	return f.pending, nil
}
func (f *fakeStore) MarkSent(_ context.Context, id, _ string) error {
	f.sent = append(f.sent, id)
	return nil
}
func (f *fakeStore) MarkQuarantined(_ context.Context, id, _ string) error {
	f.quarantined = append(f.quarantined, id)
	return nil
}
func (f *fakeStore) RecordFailure(_ context.Context, id, _ string) error {
	f.failed = append(f.failed, id)
	return nil
}

func row(id string) persistence.OutboxRow {
	return persistence.OutboxRow{ID: id, RoutingKey: "some.event", Payload: []byte(`{}`), Status: "pending"}
}

func newRelay(store Store, validate Validator, publish Publisher) *Relay {
	return New(Config{
		Store:    store,
		Validate: validate,
		Publish:  publish,
		Interval: time.Millisecond,
		Batch:    100,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return time.Unix(0, 0).UTC() },
	})
}

var okValidate = func([]byte) error { return nil }
var okPublish = func(context.Context, string, []byte) error { return nil }

// Valid rows publish and flip to sent (sent only after a successful ack).
func TestDrainOnce_PublishesAndMarksSent(t *testing.T) {
	store := &fakeStore{pending: []persistence.OutboxRow{row("a"), row("b")}}
	r := newRelay(store, okValidate, okPublish)

	sent, err := r.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if sent != 2 || len(store.sent) != 2 {
		t.Fatalf("sent = %d / %v, want 2", sent, store.sent)
	}
}

// A row that fails validation is quarantined, never published, and does NOT block
// healthy rows behind it.
func TestDrainOnce_InvalidRowQuarantinedAndDoesNotBlock(t *testing.T) {
	store := &fakeStore{pending: []persistence.OutboxRow{row("bad"), row("good")}}
	published := []string{}
	validate := func(p []byte) error {
		// First row is invalid; second is fine.
		if len(store.quarantined) == 0 && len(published) == 0 {
			return errors.New("envelope: bad correlationId")
		}
		return nil
	}
	publish := func(_ context.Context, rk string, _ []byte) error {
		published = append(published, rk)
		return nil
	}
	r := newRelay(store, validate, publish)

	sent, err := r.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if len(store.quarantined) != 1 || store.quarantined[0] != "bad" {
		t.Fatalf("quarantined = %v, want [bad]", store.quarantined)
	}
	if sent != 1 || len(store.sent) != 1 || store.sent[0] != "good" {
		t.Fatalf("sent = %v, want [good] (healthy row must not be blocked by the quarantined one)", store.sent)
	}
	if len(published) != 1 {
		t.Fatalf("published %d times, want 1 (the quarantined row must never be published)", len(published))
	}
}

// A broker-unreachable publish keeps the row pending (RecordFailure, not MarkSent) and
// stops the batch so the relay backs off and retries.
func TestDrainOnce_PublishFailureKeepsPending(t *testing.T) {
	store := &fakeStore{pending: []persistence.OutboxRow{row("a"), row("b")}}
	publish := func(context.Context, string, []byte) error { return errors.New("broker unreachable") }
	r := newRelay(store, okValidate, publish)

	sent, err := r.DrainOnce(context.Background())
	if err == nil {
		t.Fatalf("expected a publish error to surface for backoff")
	}
	if sent != 0 || len(store.sent) != 0 {
		t.Fatalf("sent = %d, want 0 (never mark sent without an ack)", sent)
	}
	if len(store.failed) == 0 {
		t.Fatalf("expected RecordFailure to be called so the row stays pending")
	}
}
