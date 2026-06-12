// Package consumer is the inbound seam: it validates each consumed message
// against /contract, dedupes it through the idempotent inbox, folds a
// lap.recorded into the standings projection (all atomically), then acks — and
// signals the web layer when the read-model actually changed. It works against
// the broker-agnostic messaging.Delivery interface so it is unit-testable with a
// fake delivery (no RabbitMQ needed).
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
// (NFR6); a transient store error requeues; an invalid/undecodable message is
// nacked without requeue (no poison loop — Story 1.9 adds the DLX that turns
// this into a true dead-letter); an unhandled-but-valid type is acked + ignored
// (tolerant reader).
func (h *Handler) Process(ctx context.Context, d messaging.Delivery) {
	body := d.Body()

	if err := h.Validate(body); err != nil {
		h.Log.Error("rejecting invalid message (failed /contract validation on consume)", "error", err.Error())
		h.nack(d, false)
		return
	}

	env, err := messaging.DecodeIncoming(body)
	if err != nil {
		h.Log.Error("rejecting undecodable envelope", "error", err.Error())
		h.nack(d, false)
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
		h.nack(d, false)
		return
	}
	if !h.sessionIDOK(d, env, data.SessionID) {
		return
	}

	lap := domain.Lap{
		MasterID:  data.MasterID,
		LapTimeMs: data.LapTimeMs,
		At:        data.At,
		Seq:       h.seq.Add(1),
	}
	applied, duplicate, err := h.Store.ApplyLap(ctx, env.ID, env.Type, h.now(), data.SessionID, lap)
	if !h.settle(d, env, applied, duplicate, err, "lap") {
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
		h.nack(d, false)
		return
	}
	if !h.sessionIDOK(d, env, data.SessionID) {
		return
	}
	applied, duplicate, err := h.Store.ApplySessionStarted(ctx, env.ID, env.Type, h.now(), data.SessionID, data.StartedAt)
	if !h.settle(d, env, applied, duplicate, err, "session.started") {
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
		h.nack(d, false)
		return
	}
	if !h.sessionIDOK(d, env, data.SessionID) {
		return
	}
	applied, duplicate, err := h.Store.ApplySessionEnded(ctx, env.ID, env.Type, h.now(), data.SessionID, data.EndedAt)
	if !h.settle(d, env, applied, duplicate, err, "session.ended") {
		return
	}
	h.Log.Info("session ended", "eventId", env.ID, "sessionId", data.SessionID,
		"boardChanged", applied, "correlationId", env.CorrelationID)
}

// sessionIDOK guards the one wire field the read-model keys on that /contract
// does not length-pin (sessionId has no minLength, unlike masterId's pattern):
// an empty/blank sessionId would implicit-create a nameless current board, so
// the event is rejected exactly like an invalid message (logged + nacked
// without requeue — never applied, never silently dropped).
func (h *Handler) sessionIDOK(d messaging.Delivery, env messaging.IncomingEnvelope, sessionID string) bool {
	if strings.TrimSpace(sessionID) != "" {
		return true
	}
	h.Log.Error("rejecting "+env.Type+" with empty sessionId", "eventId", env.ID,
		"correlationId", env.CorrelationID)
	h.nack(d, false)
	return false
}

// settle is the shared ack/nack + notify discipline: a transient store error
// requeues (no ack — NFR6); success acks AFTER the commit; a duplicate is a
// logged no-op; a read-model change notifies the web layer. Returns true for
// any committed non-duplicate — including applied=false no-ops — so the caller
// can emit its domain log line (which carries the applied flag).
func (h *Handler) settle(d messaging.Delivery, env messaging.IncomingEnvelope, applied, duplicate bool, err error, what string) bool {
	if err != nil {
		// Transient (e.g. DB) failure: do NOT ack; requeue for another attempt.
		h.Log.Error("failed to apply "+what+"; requeueing", "error", err.Error(), "eventId", env.ID)
		h.nack(d, true)
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
