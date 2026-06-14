// Package heartbeat emits the 1 s liveness signal and maintains the liveness
// touch-file the Docker healthcheck reads (blueprint §Liveness; ADR-0004 bus-only
// health). Dependencies are injected so the loop is unit-testable without a broker.
// Blueprint mechanics only — the service supplies its own Build/Validate/Publish.
package heartbeat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// Builder builds a heartbeat envelope stamped at the given time.
type Builder func(now time.Time) messaging.Envelope

// Validator validates an envelope against /contract; non-nil means do not publish.
type Validator func(messaging.Envelope) error

// Publisher publishes a body to a routing key on the owned exchange.
type Publisher func(ctx context.Context, routingKey string, body []byte) error

// Emitter ticks every Interval, building → validating → publishing a heartbeat and
// then touching LivenessFile. An invalid heartbeat is logged + dropped (never
// published) and does NOT touch the liveness file.
type Emitter struct {
	Interval     time.Duration
	LivenessFile string
	Build        Builder
	Validate     Validator
	Publish      Publisher
	Log          *slog.Logger
	Now          func() time.Time // injectable clock (defaults to time.Now)
}

// Run blocks, emitting heartbeats until ctx is cancelled. It emits one heartbeat
// immediately, then on every tick. It returns nil on graceful cancellation.
func (e *Emitter) Run(ctx context.Context) error {
	now := e.Now
	if now == nil {
		now = time.Now
	}
	if e.Log == nil { // a reusable lib must not panic if a consumer omits the logger
		e.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ticker := time.NewTicker(e.Interval)
	defer ticker.Stop()

	e.emitOnce(ctx, now()) // beat immediately so liveness is established at t0
	for {
		select {
		case <-ctx.Done():
			e.Log.Info("heartbeat loop stopped")
			return nil
		case <-ticker.C:
			e.emitOnce(ctx, now())
		}
	}
}

func (e *Emitter) emitOnce(ctx context.Context, t time.Time) {
	env := e.Build(t)
	if err := e.Validate(env); err != nil {
		// Blueprint: invalid out -> log + drop, never publish.
		e.Log.Error("dropping invalid heartbeat (failed /contract validation)", "error", err.Error())
		return
	}
	body, err := json.Marshal(env)
	if err != nil {
		e.Log.Error("failed to marshal heartbeat", "error", err.Error())
		return
	}
	if err := e.Publish(ctx, messaging.HeartbeatRoutingKey, body); err != nil {
		e.Log.Error("failed to publish heartbeat", "error", err.Error())
		return
	}
	if err := touch(e.LivenessFile, t); err != nil {
		e.Log.Error("failed to update liveness file", "error", err.Error(), "file", e.LivenessFile)
		return
	}
	e.Log.Debug("heartbeat published", "routingKey", messaging.HeartbeatRoutingKey)
}

// touch writes the timestamp to the liveness file, updating its mtime. The healthcheck
// treats a fresh mtime as proof the heartbeat loop (and thus the bus connection) is
// alive.
func touch(path string, t time.Time) error {
	return os.WriteFile(path, []byte(messaging.FormatWireTime(t)+"\n"), 0o644)
}
