package heartbeat

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func newEmitter(t *testing.T, validate Validator, publish Publisher) (*Emitter, string) {
	t.Helper()
	live := filepath.Join(t.TempDir(), "live")
	e := &Emitter{
		Interval:     5 * time.Millisecond,
		LivenessFile: live,
		Build: func(now time.Time) messaging.Envelope {
			return messaging.NewHeartbeatEnvelope("timing", "inst", "cid", now)
		},
		Validate: validate,
		Publish:  publish,
		Log:      quietLogger(),
	}
	return e, live
}

func TestEmitter_PublishesAndTouchesLivenessFile(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	publish := func(ctx context.Context, rk string, body []byte) error {
		mu.Lock()
		keys = append(keys, rk)
		mu.Unlock()
		return nil
	}
	e, live := newEmitter(t, func(messaging.Envelope) error { return nil }, publish)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = e.Run(ctx); close(done) }()
	time.Sleep(40 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	n := len(keys)
	firstKey := ""
	if n > 0 {
		firstKey = keys[0]
	}
	mu.Unlock()

	if n < 2 {
		t.Fatalf("expected multiple heartbeats, got %d", n)
	}
	if firstKey != messaging.HeartbeatRoutingKey {
		t.Errorf("routing key = %q, want %q", firstKey, messaging.HeartbeatRoutingKey)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("liveness file was not created: %v", err)
	}
}

// An invalid heartbeat must be dropped: not published, and the liveness file must
// not be touched (blueprint: invalid out -> log + drop).
func TestEmitter_DropsInvalidHeartbeat_NoPublishNoTouch(t *testing.T) {
	published := false
	publish := func(ctx context.Context, rk string, body []byte) error { published = true; return nil }
	e, live := newEmitter(t, func(messaging.Envelope) error { return errInvalid }, publish)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = e.Run(ctx); close(done) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	if published {
		t.Error("an invalid heartbeat must NOT be published")
	}
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Error("liveness file must not be touched when the heartbeat is invalid")
	}
}

func TestEmitter_StopsOnContextCancel(t *testing.T) {
	e, _ := newEmitter(t, func(messaging.Envelope) error { return nil },
		func(ctx context.Context, rk string, body []byte) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error on graceful cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

const errInvalid = sentinelErr("invalid heartbeat")
