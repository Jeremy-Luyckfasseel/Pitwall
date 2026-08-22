package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/dlq"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/consumer"
)

// fakeRefresher records the refresh calls (and can be made to fail).
type fakeRefresher struct {
	calls []consumer.PRUpdated
	err   error
}

func (f *fakeRefresher) Refresh(_ context.Context, u consumer.PRUpdated) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, u)
	return nil
}

func prUpdatedBody(id, masterID string) []byte {
	return []byte(`{"id":"` + id + `","type":"driver.pr_updated","source":"driver","schemaVersion":1,` +
		`"envelopeVersion":1,"occurredAt":"2026-06-05T14:03:21.512Z",` +
		`"correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,` +
		`"data":{"masterId":"` + masterID + `","lapTimeMs":41980,"setAt":"2026-05-31T14:03:21.500Z"}}`)
}

func newRefreshHandler(t *testing.T, r consumer.Refresher) *consumer.PRRefreshHandler {
	t.Helper()
	return &consumer.PRRefreshHandler{
		Validate:  func([]byte) error { return nil },
		Refresher: r,
		Log:       quietLog(),
		Key:       "driver.pr_updated",
		Policy:    dlq.Policy{MaxAttempts: 5, BaseMs: 1000, Multiplier: 2, MaxMs: 60000},
		Retry:     func(context.Context, []byte, int, int) error { return nil },
		Park:      func(context.Context, []byte, string) error { return nil },
	}
}

// AC2: a valid driver.pr_updated is refreshed into the local copy and acked.
func TestPRRefresh_RefreshesAndAcks(t *testing.T) {
	r := &fakeRefresher{}
	h := newRefreshHandler(t, r)
	d := &fakeDelivery{body: prUpdatedBody("018f7a2c-9d3e-7c41-b8a2-1e6f4d2c5b09", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")}
	h.Process(context.Background(), d)
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 refresh call, got %d", len(r.calls))
	}
	if r.calls[0].MasterID != "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21" || r.calls[0].LapTimeMs != 41980 {
		t.Errorf("refresh call = %+v, want masterId + lapTimeMs 41980", r.calls[0])
	}
	if !d.acked {
		t.Error("a successful refresh must ack the delivery")
	}
}

// An invalid message parks immediately (never retried as poison).
func TestPRRefresh_InvalidParks(t *testing.T) {
	r := &fakeRefresher{}
	h := newRefreshHandler(t, r)
	h.Validate = func([]byte) error { return errors.New("bad") }
	d := &fakeDelivery{body: []byte(`{"type":"driver.pr_updated","data":"BAD"}`)}
	h.Process(context.Background(), d)
	if len(r.calls) != 0 {
		t.Error("invalid message must not reach the refresher")
	}
	if !d.acked {
		t.Error("a parked message is acked after park publish")
	}
}

// An unhandled-but-valid type is acked + ignored (tolerant reader).
func TestPRRefresh_UnhandledTypeIgnored(t *testing.T) {
	r := &fakeRefresher{}
	h := newRefreshHandler(t, r)
	body := []byte(`{"id":"018f7a2c-9d3e-7c41-b8a2-1e6f4d2c5b09","type":"driver.profile_updated","source":"driver",` +
		`"schemaVersion":1,"envelopeVersion":1,"occurredAt":"2026-06-05T14:03:21.512Z",` +
		`"correlationId":"8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55","causationId":null,"data":{}}`)
	h.Process(context.Background(), &fakeDelivery{body: body})
	if len(r.calls) != 0 {
		t.Error("an unhandled type must not reach the refresher")
	}
}

// A processing (store) failure retries below the cap.
func TestPRRefresh_ProcessingFailureRetries(t *testing.T) {
	r := &fakeRefresher{err: errors.New("db boom")}
	retried := false
	h := newRefreshHandler(t, r)
	h.Retry = func(context.Context, []byte, int, int) error { retried = true; return nil }
	d := &fakeDelivery{retries: 0, body: prUpdatedBody("id-r", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")}
	h.Process(context.Background(), d)
	if !retried {
		t.Error("a processing failure below the cap must schedule a retry")
	}
	if !d.acked {
		t.Error("after scheduling a retry the original is acked")
	}
}

// A processing failure at the cap parks.
func TestPRRefresh_ProcessingFailureParksAtCap(t *testing.T) {
	r := &fakeRefresher{err: errors.New("db boom")}
	parked := false
	h := newRefreshHandler(t, r)
	h.Park = func(context.Context, []byte, string) error { parked = true; return nil }
	d := &fakeDelivery{retries: 4, body: prUpdatedBody("id-cap", "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")}
	h.Process(context.Background(), d)
	if !parked {
		t.Error("at the retry cap the message must park")
	}
}
