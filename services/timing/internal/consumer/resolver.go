package consumer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	gopdb "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
)

// PermanentPublishError marks a lookup-publish failure that retrying cannot fix — a
// malformed or contract-invalid envelope (a programmer error), as opposed to a transient
// connectivity failure (bus down). Resolve returns it immediately so the simulator's
// Prepare genuinely fails fast instead of re-publishing an un-publishable lookup forever.
// The publish seam (cmd/timing) wraps marshal/validate failures with Permanent(...).
type PermanentPublishError struct{ Err error }

func (e *PermanentPublishError) Error() string {
	return fmt.Sprintf("permanent publish failure: %v", e.Err)
}
func (e *PermanentPublishError) Unwrap() error { return e.Err }

// Permanent wraps err as a PermanentPublishError (retrying will not help).
func Permanent(err error) error { return &PermanentPublishError{Err: err} }

func isPermanent(err error) bool {
	var p *PermanentPublishError
	return errors.As(err, &p)
}

// Resolver is Timing's register-first client: it bridges the simulator's synchronous
// Resolve(ctx, email) to the asynchronous bus request/reply. Resolve publishes an
// identity.lookup_requested (impersonating the Frontend registration producer) and
// blocks on a per-requestId waiter channel; the inbound consumer calls Deliver when the
// matching identity.resolved arrives, which (after an idempotent-inbox commit) signals
// the waiter. It is the production Deliverer for the Handler.
type Resolver struct {
	DB            *sql.DB
	Publish       func(ctx context.Context, env messaging.Envelope) error
	Source        string           // lookup envelope source ("frontend" — the registration stand-in)
	NewReqID      func() string    // request-id generator (defaults to lowercase UUID v4)
	Now           func() time.Time // clock for the lookup envelope (defaults to time.Now)
	RetryInterval time.Duration    // re-publish the lookup if no reply arrives within this (default 3s)
	Log           *slog.Logger

	mu      sync.Mutex
	waiters map[string]chan string // requestId -> buffered(1) result channel
}

func (r *Resolver) newReqID() string {
	if r.NewReqID != nil {
		return r.NewReqID()
	}
	return uuid.NewString() // lowercase UUID v4 — satisfies the requestId pattern
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Resolver) register(requestID string) chan string {
	ch := make(chan string, 1)
	r.mu.Lock()
	if r.waiters == nil {
		r.waiters = make(map[string]chan string)
	}
	r.waiters[requestID] = ch
	r.mu.Unlock()
	return ch
}

func (r *Resolver) unregister(requestID string) {
	r.mu.Lock()
	delete(r.waiters, requestID)
	r.mu.Unlock()
}

func (r *Resolver) signal(requestID, masterID string) bool {
	r.mu.Lock()
	ch, ok := r.waiters[requestID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- masterID:
	default:
		// Already signalled — never block the consumer goroutine. This is reached when a
		// retry-published lookup drew a SECOND identity.resolved for a waiter the first
		// reply already satisfied; Identity is idempotent (same email -> same masterId,
		// Story 2.2) so the dropped value equals the delivered one.
		if r.Log != nil {
			r.Log.Debug("waiter already signalled; dropping duplicate masterId", "requestId", requestID)
		}
	}
	return true
}

// Resolve performs the register-first lookup for email and returns the canonical
// masterId once Identity replies (correlated by requestId). It blocks until the reply
// is delivered or ctx is done — register-first: a driver never goes on track without a
// resolved id. It RE-PUBLISHES the lookup every RetryInterval until a reply arrives, so
// a lookup lost before Identity bound its queue (startup race), or Identity restarting,
// recovers rather than hanging forever. The requestId is stable across retries; Identity
// resolves idempotently (same email -> same id) and the consumer dedupes the replies.
func (r *Resolver) Resolve(ctx context.Context, email string) (string, error) {
	requestID := r.newReqID()
	ch := r.register(requestID)
	defer r.unregister(requestID)

	interval := r.RetryInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	const alertEvery = 5 // escalate a stall to an alert log every N attempts

	for attempts := 1; ; attempts++ {
		// The lookup is flow-originating; use the requestId as the correlationId (a
		// lowercase UUID, valid per the envelope schema) so the flow is traceable.
		env := messaging.NewLookupRequestedEnvelope(r.Source, requestID, requestID, email, r.now())
		if err := r.Publish(ctx, env); err != nil {
			if isPermanent(err) {
				// A malformed/contract-invalid lookup — retrying cannot help. Fail fast so
				// the simulator's Prepare surfaces it loudly rather than spinning forever.
				return "", err
			}
			// Transient (e.g. bus down): wait and retry unless ctx is done.
			if r.Log != nil {
				r.Log.Warn("publishing identity.lookup_requested failed; will retry", "error", err.Error(), "email", email)
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(interval):
				continue
			}
		}

		select {
		case masterID := <-ch:
			return masterID, nil
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
			// No reply yet — re-publish (the first lookup may have raced Identity's bind,
			// or Identity may be down). Escalate periodically so a real stall is visible
			// (the blocking is intentional — register-first never goes on track without an id).
			if r.Log != nil && attempts%alertEvery == 0 {
				r.Log.Error("still awaiting identity.resolved after repeated lookups — is Identity up?",
					"alert", "identity_resolve_stalled", "email", email, "attempts", attempts)
			}
		}
	}
}

// Deliver records the resolved reply idempotently (inbox dedupe on the envelope id) and,
// if fresh, signals the waiting Resolve caller. A redelivery (same envelope id) returns
// duplicate=true and is a no-op. A reply with no live waiter is graceful (logged, not an
// error): the requester already moved on, or it is a stray replay.
//
// Recovery model (the in-memory signal is OUTSIDE the inbox tx, so a signal lost to a
// race would leave that envelope id deduped and unrecoverable from THAT reply): Resolve
// re-publishes the lookup under a STABLE requestId, so Identity (idempotent — same email
// -> same masterId, Story 2.2) re-replies under a FRESH envelope id that is NOT
// inbox-deduped and reaches the still-registered waiter. Across a Resolve restart the
// requestId is fresh too. The inbox therefore only suppresses exact redeliveries of the
// SAME reply (correct), never the recovery path. Recovery rests on Identity idempotency +
// a new envelope id per reply; nothing here enforces a violation, hence this note.
func (r *Resolver) Deliver(ctx context.Context, envID, envType, processedAt string, res Resolved) (bool, error) {
	dup := false
	err := persistence.WithinTx(ctx, r.DB, func(tx *sql.Tx) error {
		seen, err := gopdb.InboxHasSeen(ctx, tx, envID)
		if err != nil {
			return err
		}
		if seen {
			dup = true
			return nil
		}
		return gopdb.InboxMarkSeen(ctx, tx, envID, envType, processedAt)
	})
	if err != nil {
		return false, err
	}
	if dup {
		return true, nil
	}
	if !r.signal(res.RequestID, res.MasterID) && r.Log != nil {
		r.Log.Warn("identity.resolved with no live waiter; ignoring",
			"requestId", res.RequestID, "masterId", res.MasterID)
	}
	return false, nil
}
