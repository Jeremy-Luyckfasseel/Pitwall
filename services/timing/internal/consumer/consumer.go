// Package consumer is Timing's inbound seam (its FIRST, arriving in Story 2.3): it
// validates each consumed identity.resolved against /contract, then hands it to a
// Deliverer that — in ONE transaction — dedupes on the envelope id (idempotent inbox)
// and, if fresh, delivers the resolved masterId to the waiting register-first caller
// (the simulator's Resolve). It works against the broker-agnostic messaging.Delivery
// interface and a narrow Deliverer interface, so it is unit-testable with fakes (no
// RabbitMQ, no DB).
//
// Failure handling (Story 1.9 blueprint spine) routes through the consumer-side DLQ
// rather than dropping: an INVALID/undecodable message is parked immediately (never
// retried as poison); a PROCESSING failure (the inbox tx errors) is retried with
// exponential backoff and, at the cap, parked. Every park is logged with an alert —
// "logged + dead-lettered, never silently dropped" (NFR4/M5).
package consumer

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/dlq"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
)

// Resolved is the identity.resolved data payload Timing consumes.
type Resolved struct {
	RequestID string `json:"requestId"`
	Email     string `json:"email"`
	MasterID  string `json:"masterId"`
}

// Deliverer records a resolved reply idempotently (inbox dedupe on the envelope id) and,
// if fresh, hands the masterId to the waiting Resolve caller. It returns duplicate=true
// when the envelope id was already processed (a redelivery → safe no-op). The Resolver
// (resolver.go) is the production implementation; the Handler is unit-tested with a fake.
type Deliverer interface {
	Deliver(ctx context.Context, envID, envType, processedAt string, r Resolved) (duplicate bool, err error)
}

// Handler processes one delivery at a time (a single Run goroutine drives it).
type Handler struct {
	Validate    func([]byte) error // validate-on-consume (envelope + data); nil err = valid
	Deliverer   Deliverer
	Log         *slog.Logger
	ResolvedKey string // == messaging.IdentityResolvedRoutingKey

	// DLQ wiring (Story 1.9). Policy decides retry-vs-park from the redelivery count;
	// Retry republishes to the retry queue with backoff; Park routes terminally to the
	// parking quarantine. Retry/Park are injected (wrap messaging.Bus in production).
	Policy dlq.Policy
	Retry  func(ctx context.Context, body []byte, delayMs, nextRetries int) error
	Park   func(ctx context.Context, body []byte, reason string) error
}

// Run consumes deliveries until the channel closes or ctx is cancelled.
func (h *Handler) Run(ctx context.Context, deliveries <-chan messaging.Delivery) {
	for {
		select {
		case <-ctx.Done():
			h.Log.Info("identity.resolved consumer loop stopped")
			return
		case d, ok := <-deliveries:
			if !ok {
				h.Log.Info("identity.resolved delivery channel closed")
				return
			}
			h.Process(ctx, d)
		}
	}
}

// Process handles a single delivery. Every rejection is LOGGED (never a silent drop).
// Ack only AFTER the inbox tx commits; an invalid/undecodable message parks immediately;
// a processing failure retries then parks; an unhandled-but-valid type is acked + ignored.
func (h *Handler) Process(ctx context.Context, d messaging.Delivery) {
	body := d.Body()

	if err := h.Validate(body); err != nil {
		h.Log.Error("rejecting invalid message (failed /contract validation on consume)", "error", err.Error())
		h.park(ctx, d, "contract-invalid", "", "")
		return
	}

	env, err := messaging.DecodeIncoming(body)
	if err != nil {
		h.Log.Error("rejecting undecodable envelope", "error", err.Error())
		h.park(ctx, d, "undecodable-envelope", "", "")
		return
	}

	if env.Type != h.ResolvedKey {
		// Tolerant reader: a valid type Timing does not handle. Drop it.
		h.Log.Debug("ignoring unhandled event type", "type", env.Type, "eventId", env.ID)
		h.ack(d)
		return
	}

	var data Resolved
	if err := json.Unmarshal(env.Data, &data); err != nil {
		h.Log.Error("rejecting identity.resolved with undecodable data", "error", err.Error(), "eventId", env.ID)
		h.park(ctx, d, "undecodable-data", env.ID, env.CorrelationID)
		return
	}

	dup, err := h.Deliverer.Deliver(ctx, env.ID, env.Type, env.OccurredAt, data)
	if err != nil {
		h.retryOrPark(ctx, d, env, err)
		return
	}

	h.ack(d)
	if dup {
		h.Log.Debug("duplicate identity.resolved ignored (idempotent inbox)", "eventId", env.ID)
		return
	}
	h.Log.Info("identity resolved delivered to register-first waiter",
		"eventId", env.ID, "masterId", data.MasterID, "requestId", data.RequestID, "correlationId", env.CorrelationID)
}

// retryOrPark handles a processing failure: below the cap it republishes to the retry
// queue with the backoff TTL then acks the original; at the cap it parks (+ alert) and
// acks. A failed retry/park PUBLISH does NOT ack — it requeues so a broker hiccup
// mid-republish never loses the message (NFR6).
func (h *Handler) retryOrPark(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope, cause error) {
	dec := dlq.NextRetry(d.RetryCount(), h.Policy)
	if dec.Park {
		h.Log.Error("delivering identity.resolved kept failing; parking after exhausting retries",
			"error", cause.Error(), "eventId", env.ID, "maxAttempts", h.Policy.MaxAttempts)
		h.park(ctx, d, "retries-exhausted", env.ID, env.CorrelationID)
		return
	}
	if h.Retry == nil {
		h.nack(d, true) // defensive: no retry sink wired -> keep the message
		return
	}
	if err := h.Retry(ctx, d.Body(), dec.DelayMs, dec.NextRetries); err != nil {
		h.Log.Error("failed to schedule DLQ retry; requeueing", "error", err.Error(), "eventId", env.ID)
		h.nack(d, true)
		return
	}
	h.Log.Warn("delivering identity.resolved failed; scheduled DLQ retry", "error", cause.Error(),
		"eventId", env.ID, "retryInMs", dec.DelayMs, "attempt", dec.NextRetries, "correlationId", env.CorrelationID)
	h.ack(d)
}

// park routes a message terminally to the parking quarantine and emits the alert. The
// original is acked only after the park publish succeeds; a publish failure requeues it
// (never lost). With no Park sink wired it falls back to a no-requeue nack so the work
// queue's DLX safety net captures it — never an ack-drop.
func (h *Handler) park(ctx context.Context, d messaging.Delivery, reason, eventID, correlationID string) {
	if h.Park == nil {
		h.nack(d, false)
		return
	}
	if err := h.Park(ctx, d.Body(), reason); err != nil {
		h.Log.Error("failed to park message; requeueing", "error", err.Error(), "reason", reason, "eventId", eventID)
		h.nack(d, true)
		return
	}
	h.Log.Error("message parked (quarantined); not retried", "alert", "message_parked",
		"reason", reason, "eventId", eventID, "correlationId", correlationID)
	h.ack(d)
}

func (h *Handler) ack(d messaging.Delivery) {
	if err := d.Ack(); err != nil {
		h.Log.Error("failed to ack delivery", "error", err.Error())
	}
}

func (h *Handler) nack(d messaging.Delivery, requeue bool) {
	if err := d.Nack(requeue); err != nil {
		h.Log.Error("failed to nack delivery", "error", err.Error(), "requeue", requeue)
	}
}
