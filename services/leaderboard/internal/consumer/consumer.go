// Package consumer is the inbound seam: it validates each consumed message
// against /contract, dedupes it through the idempotent inbox, folds a
// lap.recorded into the standings projection (all atomically), then acks — and
// signals the web layer when the read-model actually changed. It works against
// the broker-agnostic messaging.Delivery interface so it is unit-testable with a
// fake delivery (no RabbitMQ needed).
//
// Failure handling (Story 1.9) routes through the consumer-side DLQ rather than
// dropping: an INVALID message (bad /contract, undecodable, blank sessionId) is
// parked immediately (never retried as poison); a PROCESSING failure (the store
// errors) is retried with exponential backoff and, on exceeding the cap, parked.
// Every park emits a Control-Room-bound alert — "logged + dead-lettered, never
// silently dropped" (NFR4/M5). Parking/retry are injected funcs so the handler
// stays broker-agnostic and unit-testable.
package consumer

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/messaging"
)

// applier is the persistence surface the handler needs (satisfied by
// *persistence.Store). Narrow interface keeps the consumer unit-testable.
type applier interface {
	ApplyLap(ctx context.Context, envelopeID, eventType, processedAt, sessionID string, lap domain.Lap) (applied bool, duplicate bool, err error)
	ApplySessionStarted(ctx context.Context, envelopeID, eventType, processedAt, sessionID, startedAt string) (applied bool, duplicate bool, err error)
	ApplySessionEnded(ctx context.Context, envelopeID, eventType, processedAt, sessionID, endedAt string) (applied bool, duplicate bool, err error)
}

// Handler processes one delivery at a time. A single Run goroutine drives it, so
// the ingest sequence counter needs no locking beyond the atomic (which also
// guards the rare concurrent call in tests).
type Handler struct {
	Validate func([]byte) error // validate-on-consume (envelope + data); nil err = valid
	Store    applier
	Log      *slog.Logger
	Notify   func()        // called after the read-model actually changes (optional)
	Now      func() string // processedAt stamp; defaults to wire-now
	StartSeq int64         // seed the ingest sequence (e.g. max stored seq) for restart monotonicity

	// DLQ wiring (Story 1.9). Policy decides retry-vs-park from the redelivery
	// count; Retry republishes a failed message to the retry queue with the
	// backoff TTL; Park routes a message terminally to the parking quarantine.
	// Retry/Park are injected (wrap messaging.Bus in production; spies in tests).
	Policy domain.DLQPolicy
	Retry  func(ctx context.Context, body []byte, delayMs, nextRetries int) error
	Park   func(ctx context.Context, body []byte, reason string) error

	seq atomic.Int64
}

// Run consumes deliveries until the channel closes or ctx is cancelled.
func (h *Handler) Run(ctx context.Context, deliveries <-chan messaging.Delivery) {
	h.seq.Store(h.StartSeq)
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

// Process handles a single delivery. Every rejection is LOGGED (never a silent
// drop). The ack/nack discipline: ack only AFTER the local state change commits
// (NFR6); a processing failure is retried then parked (Story 1.9); an
// invalid/undecodable message is parked immediately (not retried as poison); an
// unhandled-but-valid type is acked + ignored (tolerant reader).
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

	switch env.Type {
	case messaging.LapRecordedRoutingKey:
		h.processLap(ctx, d, env)
	case messaging.SessionStartedRoutingKey:
		h.processSessionStarted(ctx, d, env)
	case messaging.SessionEndedRoutingKey:
		h.processSessionEnded(ctx, d, env)
	default:
		// Tolerant reader: a valid type the board does not project. Drop it.
		h.Log.Debug("ignoring unhandled event type", "type", env.Type, "eventId", env.ID)
		h.ack(d)
	}
}

func (h *Handler) processLap(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope) {
	data, err := messaging.DecodeLapRecorded(env)
	if err != nil {
		h.Log.Error("rejecting lap.recorded with undecodable data", "error", err.Error(), "eventId", env.ID)
		h.park(ctx, d, "undecodable-data", env.ID, env.CorrelationID)
		return
	}
	if !h.sessionIDOK(ctx, d, env, data.SessionID) {
		return
	}

	lap := domain.Lap{
		MasterID:  data.MasterID,
		LapTimeMs: data.LapTimeMs,
		At:        data.At,
		Seq:       h.seq.Add(1),
	}
	applied, duplicate, err := h.Store.ApplyLap(ctx, env.ID, env.Type, h.now(), data.SessionID, lap)
	if !h.settle(ctx, d, env, applied, duplicate, err, "lap") {
		return
	}
	h.Log.Debug("lap applied", "eventId", env.ID, "masterId", data.MasterID, "sessionId", data.SessionID,
		"lapTimeMs", data.LapTimeMs, "improvedBest", applied, "correlationId", env.CorrelationID)
}

// processSessionStarted applies a session.started: a new session becomes the
// current (auto-reset, FR43) ACTIVE board; a known one reconciles forward-only
// (a replayed start never wipes a live board — NFR24).
func (h *Handler) processSessionStarted(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope) {
	data, err := messaging.DecodeSessionStarted(env)
	if err != nil {
		h.Log.Error("rejecting session.started with undecodable data", "error", err.Error(), "eventId", env.ID)
		h.park(ctx, d, "undecodable-data", env.ID, env.CorrelationID)
		return
	}
	if !h.sessionIDOK(ctx, d, env, data.SessionID) {
		return
	}
	applied, duplicate, err := h.Store.ApplySessionStarted(ctx, env.ID, env.Type, h.now(), data.SessionID, data.StartedAt)
	if !h.settle(ctx, d, env, applied, duplicate, err, "session.started") {
		return
	}
	h.Log.Info("session started", "eventId", env.ID, "sessionId", data.SessionID,
		"boardChanged", applied, "correlationId", env.CorrelationID)
}

// processSessionEnded applies a session.ended: the session shows FINISHED
// (FR45); an end for an unknown session is recorded, never dropped (NFR24).
func (h *Handler) processSessionEnded(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope) {
	data, err := messaging.DecodeSessionEnded(env)
	if err != nil {
		h.Log.Error("rejecting session.ended with undecodable data", "error", err.Error(), "eventId", env.ID)
		h.park(ctx, d, "undecodable-data", env.ID, env.CorrelationID)
		return
	}
	if !h.sessionIDOK(ctx, d, env, data.SessionID) {
		return
	}
	applied, duplicate, err := h.Store.ApplySessionEnded(ctx, env.ID, env.Type, h.now(), data.SessionID, data.EndedAt)
	if !h.settle(ctx, d, env, applied, duplicate, err, "session.ended") {
		return
	}
	h.Log.Info("session ended", "eventId", env.ID, "sessionId", data.SessionID,
		"boardChanged", applied, "correlationId", env.CorrelationID)
}

// sessionIDOK guards the one wire field the read-model keys on that /contract
// does not length-pin (sessionId has no minLength, unlike masterId's pattern):
// an empty/blank sessionId would implicit-create a nameless current board, so
// the event is parked exactly like an invalid message (logged + quarantined —
// never applied, never retried, never silently dropped).
func (h *Handler) sessionIDOK(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope, sessionID string) bool {
	if strings.TrimSpace(sessionID) != "" {
		return true
	}
	h.Log.Error("rejecting "+env.Type+" with empty sessionId", "eventId", env.ID,
		"correlationId", env.CorrelationID)
	h.park(ctx, d, "blank-session-id", env.ID, env.CorrelationID)
	return false
}

// settle is the shared ack/notify discipline. On a processing failure it routes
// the message through the DLQ (retry then park — Story 1.9) and returns false;
// success acks AFTER the commit; a duplicate is a logged no-op; a read-model
// change notifies the web layer. Returns true for any committed non-duplicate —
// including applied=false no-ops — so the caller can emit its domain log line.
func (h *Handler) settle(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope, applied, duplicate bool, err error, what string) bool {
	if err != nil {
		h.retryOrPark(ctx, d, env, what, err)
		return false
	}
	h.ack(d)
	if duplicate {
		h.Log.Debug("duplicate "+what+" ignored (idempotent inbox)", "eventId", env.ID)
		return false
	}
	if applied && h.Notify != nil {
		h.Notify()
	}
	return true
}

// retryOrPark handles a processing failure. Below the delivery-count cap it
// republishes the message to the retry queue with the exponential backoff TTL,
// then acks the original (it has taken ownership). At the cap it parks the
// message (+ alert) and acks. A failed retry/park PUBLISH does NOT ack — it
// requeues so a broker hiccup mid-republish never loses the message (NFR6).
func (h *Handler) retryOrPark(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope, what string, applyErr error) {
	dec := domain.NextRetry(d.RetryCount(), h.Policy)
	if dec.Park {
		h.Log.Error("apply "+what+" kept failing; parking after exhausting retries",
			"error", applyErr.Error(), "eventId", env.ID, "maxAttempts", h.Policy.MaxAttempts)
		h.park(ctx, d, "retries-exhausted", env.ID, env.CorrelationID)
		return
	}
	if h.Retry == nil {
		// Defensive: no retry sink wired → keep the message rather than drop it.
		h.nack(d, true)
		return
	}
	if err := h.Retry(ctx, d.Body(), dec.DelayMs, dec.NextRetries); err != nil {
		h.Log.Error("failed to schedule DLQ retry; requeueing", "error", err.Error(), "eventId", env.ID)
		h.nack(d, true)
		return
	}
	h.Log.Warn("apply "+what+" failed; scheduled DLQ retry", "error", applyErr.Error(),
		"eventId", env.ID, "retryInMs", dec.DelayMs, "attempt", dec.NextRetries, "correlationId", env.CorrelationID)
	h.ack(d)
}

// park routes a message terminally to the parking quarantine queue and emits the
// Control-Room-bound alert (a structured log line — placeholder until E12). The
// original is acked only after the park publish succeeds; a publish failure
// requeues it (never lost). With no Park sink wired it falls back to a no-requeue
// nack so the work queue's DLX safety net captures it — never an ack-drop.
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

func (h *Handler) now() string {
	if h.Now != nil {
		return h.Now()
	}
	return messaging.FormatWireTime(time.Now())
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
