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

	if env.Type != messaging.LapRecordedRoutingKey {
		// Tolerant reader: not ours to project (e.g. session.* is Story 1.8). Drop it.
		h.Log.Debug("ignoring unhandled event type", "type", env.Type, "eventId", env.ID)
		h.ack(d)
		return
	}

	data, err := messaging.DecodeLapRecorded(env)
	if err != nil {
		h.Log.Error("rejecting lap.recorded with undecodable data", "error", err.Error(), "eventId", env.ID)
		h.nack(d, false)
		return
	}

	lap := domain.Lap{
		MasterID:  data.MasterID,
		LapTimeMs: data.LapTimeMs,
		At:        data.At,
		Seq:       h.seq.Add(1),
	}
	applied, duplicate, err := h.Store.ApplyLap(ctx, env.ID, env.Type, h.now(), data.SessionID, lap)
	if err != nil {
		// Transient (e.g. DB) failure: do NOT ack; requeue for another attempt.
		h.Log.Error("failed to apply lap; requeueing", "error", err.Error(), "eventId", env.ID)
		h.nack(d, true)
		return
	}

	h.ack(d)
	if duplicate {
		h.Log.Debug("duplicate lap ignored (idempotent inbox)", "eventId", env.ID)
		return
	}
	if applied && h.Notify != nil {
		h.Notify()
	}
	h.Log.Debug("lap applied", "eventId", env.ID, "masterId", data.MasterID,
		"lapTimeMs", data.LapTimeMs, "improvedBest", applied, "correlationId", env.CorrelationID)
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
