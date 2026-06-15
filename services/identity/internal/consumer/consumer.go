// Package consumer is Identity's inbound seam: it validates each consumed
// identity.lookup_requested against /contract, then hands it to a Resolver that — in
// ONE transaction — dedupes on the envelope id (idempotent inbox), resolves-or-mints
// the canonical masterId, and enqueues the identity.resolved reply to the outbox
// (consumer-side ECST). It works against the broker-agnostic messaging.Delivery
// interface and a narrow Resolver interface, so it is unit-testable with fakes (no
// RabbitMQ, no DB).
//
// Failure handling (Story 1.9 blueprint spine) routes through the consumer-side DLQ
// rather than dropping: an INVALID/undecodable message is parked immediately (never
// retried as poison); a PROCESSING failure (the tx errors) is retried with exponential
// backoff and, at the cap, parked. Every park emits a Control-Room-bound alert —
// "logged + dead-lettered, never silently dropped" (NFR4/M5).
package consumer

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/messaging"
)

// ResolveResult reports the outcome of one resolve-or-mint transaction.
type ResolveResult struct {
	MasterID  string // the canonical id resolved (minted or reused)
	Minted    bool   // true if a new id was minted (false = reused an existing one)
	Duplicate bool   // true if the envelope id was already processed (inbox dedupe no-op)
}

// Resolver runs the atomic consume-side operation: inbox-dedupe → resolve-or-mint →
// enqueue the identity.resolved reply → mark inbox, all in one transaction. It is an
// interface so the Handler is unit-testable with a fake; TxResolver is the production
// implementation (resolver.go).
type Resolver interface {
	Resolve(ctx context.Context, env messaging.IncomingEnvelope, data domain.LookupData) (ResolveResult, error)
}

// Handler processes one delivery at a time (a single Run goroutine drives it).
type Handler struct {
	Validate  func([]byte) error // validate-on-consume (envelope + data); nil err = valid
	Resolver  Resolver
	Log       *slog.Logger
	Notify    func()        // kick the relay after a fresh reply is enqueued (optional)
	LookupKey string        // == messaging.LookupRequestedRoutingKey

	// DLQ wiring (Story 1.9). Policy decides retry-vs-park from the redelivery count;
	// Retry republishes to the retry queue with backoff; Park routes terminally to the
	// parking quarantine. Retry/Park are injected (wrap messaging.Bus in production).
	Policy domain.DLQPolicy
	Retry  func(ctx context.Context, body []byte, delayMs, nextRetries int) error
	Park   func(ctx context.Context, body []byte, reason string) error
}

// Run consumes deliveries until the channel closes or ctx is cancelled.
func (h *Handler) Run(ctx context.Context, deliveries <-chan messaging.Delivery) {
	for {
		select {
		case <-ctx.Done():
			h.Log.Info("consumer loop stopped")
			return
		case d, ok := <-deliveries:
			if !ok {
				h.Log.Info("consumer delivery channel closed")
				return
			}
			h.Process(ctx, d)
		}
	}
}

// Process handles a single delivery. Every rejection is LOGGED (never a silent drop).
// Ack/nack discipline: ack only AFTER the resolve-or-mint tx commits; an
// invalid/undecodable message parks immediately (not retried as poison); a processing
// failure retries then parks; an unhandled-but-valid type is acked + ignored (tolerant
// reader).
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

	if env.Type != h.LookupKey {
		// Tolerant reader: a valid type Identity does not handle. Drop it.
		h.Log.Debug("ignoring unhandled event type", "type", env.Type, "eventId", env.ID)
		h.ack(d)
		return
	}

	h.processLookup(ctx, d, env)
}

func (h *Handler) processLookup(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope) {
	var data domain.LookupData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		h.Log.Error("rejecting identity.lookup_requested with undecodable data", "error", err.Error(), "eventId", env.ID)
		h.park(ctx, d, "undecodable-data", env.ID, env.CorrelationID)
		return
	}
	// Defensive guard (validate-on-consume already enforces email minLength via
	// /contract, but never resolve on a blank natural key — it would key the store on "").
	if strings.TrimSpace(data.Email) == "" {
		h.Log.Error("rejecting identity.lookup_requested with blank email", "eventId", env.ID, "correlationId", env.CorrelationID)
		h.park(ctx, d, "blank-email", env.ID, env.CorrelationID)
		return
	}

	result, err := h.Resolver.Resolve(ctx, env, data)
	if err != nil {
		h.retryOrPark(ctx, d, env, err)
		return
	}

	h.ack(d)
	if result.Duplicate {
		h.Log.Debug("duplicate identity.lookup_requested ignored (idempotent inbox)", "eventId", env.ID)
		return
	}
	if h.Notify != nil {
		h.Notify() // a fresh reply is in the outbox; kick the relay to publish promptly
	}
	h.Log.Info("identity resolved", "eventId", env.ID, "masterId", result.MasterID,
		"minted", result.Minted, "email", data.Email, "correlationId", env.CorrelationID)
}

// retryOrPark handles a processing failure: below the cap it republishes to the retry
// queue with the backoff TTL then acks the original; at the cap it parks (+ alert) and
// acks. A failed retry/park PUBLISH does NOT ack — it requeues so a broker hiccup
// mid-republish never loses the message (NFR6).
func (h *Handler) retryOrPark(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope, cause error) {
	dec := domain.NextRetry(d.RetryCount(), h.Policy)
	if dec.Park {
		h.Log.Error("resolve kept failing; parking after exhausting retries",
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
	h.Log.Warn("resolve failed; scheduled DLQ retry", "error", cause.Error(),
		"eventId", env.ID, "retryInMs", dec.DelayMs, "attempt", dec.NextRetries, "correlationId", env.CorrelationID)
	h.ack(d)
}

// park routes a message terminally to the parking quarantine and emits the alert. The
// original is acked only after the park publish succeeds; a publish failure requeues
// it (never lost). With no Park sink wired it falls back to a no-requeue nack so the
// work queue's DLX safety net captures it — never an ack-drop.
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

// wireNow is the default processedAt/occurredAt stamp.
func wireNow() string { return messaging.FormatWireTime(time.Now()) }
