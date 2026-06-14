// Package relay is the outbox publisher loop — the reliability spine. It bridges the
// durable outbox (libs/go-pitwall/persistence) and the bus (libs/go-pitwall/messaging):
// it scans pending rows oldest-first, validates each against /contract, publishes to
// the service's own exchange, and marks a row sent ONLY after a confirmed broker ack.
//
// Failure handling is deliberate and split:
//   - validation failure  -> terminal: quarantine, never publish, never retry
//   - broker unreachable   -> transient: keep pending, back off, retry forever
//
// A service's own valid events are never poison, so a transient failure is never
// parked or dropped; only an event that cannot be validated is quarantined (a
// producer-side quarantine, distinct from the consumer-side RabbitMQ DLQ/parking).
// Blueprint mechanics only — the events, exchange and schemas are the service's.
package relay

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
)

// Store is the outbox persistence the relay drives (satisfied by
// *persistence.OutboxStore).
type Store interface {
	FetchPending(ctx context.Context, limit int) ([]persistence.OutboxRow, error)
	MarkSent(ctx context.Context, id, sentAt string) error
	MarkQuarantined(ctx context.Context, id, lastErr string) error
	RecordFailure(ctx context.Context, id, lastErr string) error
}

// Validator returns a non-nil error if a payload must NOT be published (it is then
// quarantined). Satisfied by (*messaging.Validator).ValidateEnvelopeBytes.
type Validator func(payload []byte) error

// Publisher publishes body to routingKey and returns nil ONLY on a confirmed broker
// ack. A nil error means the row may be marked sent. Satisfied by a confirm-mode
// channel's PublishConfirmed.
type Publisher func(ctx context.Context, routingKey string, body []byte) error

// maxBackoff caps the exponential backoff between retry ticks when the broker is
// unreachable. The relay keeps retrying forever; backoff just avoids busy-looping.
const maxBackoff = 5 * time.Second

// Config wires the relay's dependencies.
type Config struct {
	Store    Store
	Validate Validator
	Publish  Publisher
	Interval time.Duration
	Batch    int
	Log      *slog.Logger
	Now      func() time.Time
}

// Relay publishes outbox rows. Construct it with New.
type Relay struct {
	cfg  Config
	kick chan struct{}
}

// New builds a relay with a buffered kick channel so a freshly-enqueued row can be
// published promptly without waiting a full poll interval.
func New(cfg Config) *Relay {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 100
	}
	if cfg.Log == nil { // a reusable lib must not panic if a consumer omits the logger
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Relay{cfg: cfg, kick: make(chan struct{}, 1)}
}

// Kick signals the relay to drain promptly (best-effort, non-blocking). The poll
// ticker is the durable backstop if the kick is missed.
func (r *Relay) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run drains immediately, then on every poll tick and on every kick, until ctx is
// cancelled. It backs off (capped exponential) while the broker is unreachable and
// resets to the base interval once a drain succeeds.
func (r *Relay) Run(ctx context.Context) error {
	backoff := r.cfg.Interval
	timer := time.NewTimer(0) // first drain immediately
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			r.cfg.Log.Info("relay stopped")
			return nil
		case <-r.kick:
			if _, err := r.DrainOnce(ctx); err != nil {
				backoff = nextBackoff(backoff, r.cfg.Interval)
			} else {
				backoff = r.cfg.Interval
			}
			resetTimer(timer, backoff)
		case <-timer.C:
			if _, err := r.DrainOnce(ctx); err != nil {
				backoff = nextBackoff(backoff, r.cfg.Interval)
			} else {
				backoff = r.cfg.Interval
			}
			resetTimer(timer, backoff)
		}
	}
}

// DrainOnce processes one batch of pending rows. It returns the number marked sent and
// a non-nil error if a transient publish failure occurred (the caller backs off).
// Validation failures are terminal and do NOT surface as an error — they are
// quarantined and the drain continues with the next row.
func (r *Relay) DrainOnce(ctx context.Context) (int, error) {
	rows, err := r.cfg.Store.FetchPending(ctx, r.cfg.Batch)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, row := range rows {
		if verr := r.cfg.Validate(row.Payload); verr != nil {
			r.cfg.Log.Error("quarantining outbox row: failed /contract validation",
				"id", row.ID, "routingKey", row.RoutingKey, "error", verr.Error())
			if derr := r.cfg.Store.MarkQuarantined(ctx, row.ID, verr.Error()); derr != nil {
				return sent, derr
			}
			continue // a quarantined row must not block healthy rows behind it
		}
		if perr := r.cfg.Publish(ctx, row.RoutingKey, row.Payload); perr != nil {
			r.cfg.Log.Warn("publish failed; row stays pending, will retry",
				"id", row.ID, "routingKey", row.RoutingKey, "error", perr.Error())
			_ = r.cfg.Store.RecordFailure(ctx, row.ID, perr.Error())
			// Broker is likely down for the whole batch — stop now, back off, retry.
			return sent, perr
		}
		if merr := r.cfg.Store.MarkSent(ctx, row.ID, messaging.FormatWireTime(r.cfg.Now())); merr != nil {
			return sent, merr
		}
		sent++
	}
	return sent, nil
}

// Flush is a bounded best-effort drain for graceful shutdown. It attempts one batch
// and reports how many were sent and how many remain pending; whatever remains stays
// durably in the outbox for the next start (no loss either way).
func (r *Relay) Flush(ctx context.Context) (sent, remaining int) {
	sent, _ = r.DrainOnce(ctx)
	rows, err := r.cfg.Store.FetchPending(ctx, r.cfg.Batch)
	if err == nil {
		remaining = len(rows)
	}
	return sent, remaining
}

func nextBackoff(cur, base time.Duration) time.Duration {
	next := cur * 2
	if next > maxBackoff {
		next = maxBackoff
	}
	if next < base {
		next = base
	}
	return next
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}
