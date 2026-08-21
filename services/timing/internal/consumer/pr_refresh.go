package consumer

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/dlq"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
)

// PRUpdated is the driver.pr_updated data payload Timing consumes to refresh its local
// all-time-PR copy (Story 3.4, AC2 / FR37).
type PRUpdated struct {
	MasterID  string `json:"masterId"`
	LapTimeMs int64  `json:"lapTimeMs"`
	SetAt     string `json:"setAt"`
}

// Refresher applies a confirmed canonical PR to Timing's local copy. The production
// implementation wraps persistence.DriverPRStore.Refresh; the handler is unit-tested
// with a fake. Refresh is an idempotent overwrite (latest-confirmed-wins), so a
// redelivered driver.pr_updated re-writes the same value harmlessly — no inbox needed.
type Refresher interface {
	Refresh(ctx context.Context, u PRUpdated) error
}

// PRStoreRefresher is the production Refresher: it applies the confirmed canonical PR to
// Timing's local driver_prs copy (latest-confirmed-wins). Now supplies the wire-format
// processing timestamp (defaults to messaging.FormatWireTime(time.Now())).
type PRStoreRefresher struct {
	Store *persistence.DriverPRStore
	Now   func() string
}

func (r *PRStoreRefresher) Refresh(ctx context.Context, u PRUpdated) error {
	return r.Store.Refresh(ctx, u.MasterID, u.LapTimeMs, u.SetAt, r.Now())
}

// PRRefreshHandler is Timing's SECOND inbound consumer (Story 3.4): it validates each
// consumed driver.pr_updated against /contract and refreshes the local PR copy. It is a
// deliberately self-contained delivery processor mirroring Handler's failure spine
// (Story 1.9): an INVALID/undecodable message parks immediately; a PROCESSING failure
// retries with backoff then parks; an unhandled-but-valid type is acked + ignored —
// "logged + dead-lettered, never silently dropped".
type PRRefreshHandler struct {
	Validate  func([]byte) error // validate-on-consume (envelope + data); nil err = valid
	Refresher Refresher
	Log       *slog.Logger
	Key       string // == messaging.DriverPRUpdatedRoutingKey

	Policy dlq.Policy
	Retry  func(ctx context.Context, body []byte, delayMs, nextRetries int) error
	Park   func(ctx context.Context, body []byte, reason string) error
}

// Run consumes deliveries until the channel closes or ctx is cancelled.
func (h *PRRefreshHandler) Run(ctx context.Context, deliveries <-chan messaging.Delivery) {
	for {
		select {
		case <-ctx.Done():
			h.Log.Info("driver.pr_updated consumer loop stopped")
			return
		case d, ok := <-deliveries:
			if !ok {
				h.Log.Info("driver.pr_updated delivery channel closed")
				return
			}
			h.Process(ctx, d)
		}
	}
}

// Process handles a single delivery. Ack only AFTER the refresh succeeds; an
// invalid/undecodable message parks immediately; a processing failure retries then parks.
func (h *PRRefreshHandler) Process(ctx context.Context, d messaging.Delivery) {
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

	if env.Type != h.Key {
		h.Log.Debug("ignoring unhandled event type", "type", env.Type, "eventId", env.ID)
		h.ack(d)
		return
	}

	var data PRUpdated
	if err := json.Unmarshal(env.Data, &data); err != nil {
		h.Log.Error("rejecting driver.pr_updated with undecodable data", "error", err.Error(), "eventId", env.ID)
		h.park(ctx, d, "undecodable-data", env.ID, env.CorrelationID)
		return
	}

	if err := h.Refresher.Refresh(ctx, data); err != nil {
		h.retryOrPark(ctx, d, env, err)
		return
	}

	h.ack(d)
	h.Log.Info("local PR copy refreshed from driver.pr_updated",
		"eventId", env.ID, "masterId", data.MasterID, "lapTimeMs", data.LapTimeMs, "correlationId", env.CorrelationID)
}

func (h *PRRefreshHandler) retryOrPark(ctx context.Context, d messaging.Delivery, env messaging.IncomingEnvelope, cause error) {
	dec := dlq.NextRetry(d.RetryCount(), h.Policy)
	if dec.Park {
		h.Log.Error("refreshing local PR kept failing; parking after exhausting retries",
			"error", cause.Error(), "eventId", env.ID, "maxAttempts", h.Policy.MaxAttempts)
		h.park(ctx, d, "retries-exhausted", env.ID, env.CorrelationID)
		return
	}
	if h.Retry == nil {
		h.nack(d, true)
		return
	}
	if err := h.Retry(ctx, d.Body(), dec.DelayMs, dec.NextRetries); err != nil {
		h.Log.Error("failed to schedule DLQ retry; requeueing", "error", err.Error(), "eventId", env.ID)
		h.nack(d, true)
		return
	}
	h.Log.Warn("refreshing local PR failed; scheduled DLQ retry", "error", cause.Error(),
		"eventId", env.ID, "retryInMs", dec.DelayMs, "attempt", dec.NextRetries, "correlationId", env.CorrelationID)
	h.ack(d)
}

func (h *PRRefreshHandler) park(ctx context.Context, d messaging.Delivery, reason, eventID, correlationID string) {
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

func (h *PRRefreshHandler) ack(d messaging.Delivery) {
	if err := d.Ack(); err != nil {
		h.Log.Error("failed to ack delivery", "error", err.Error())
	}
}

func (h *PRRefreshHandler) nack(d messaging.Delivery, requeue bool) {
	if err := d.Nack(requeue); err != nil {
		h.Log.Error("failed to nack delivery", "error", err.Error(), "requeue", requeue)
	}
}
