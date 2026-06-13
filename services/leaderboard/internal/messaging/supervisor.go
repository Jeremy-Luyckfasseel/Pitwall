package messaging

import (
	"context"
	"log/slog"
	"time"
)

// connectFunc establishes ONE connection generation: it dials the broker, opens
// the channel(s), declares the service's topology (and, for a consumer, starts
// delivering), and returns a channel that closes when THIS connection dies. A
// non-nil error means the dial or setup failed — the supervisor backs off and
// retries. Each successful call re-runs the full declare hook, so the topology is
// re-asserted on a fresh broker process after a bus restart.
type connectFunc func(ctx context.Context) (closed <-chan struct{}, err error)

// backoffPolicy is the capped-exponential schedule the supervisor waits between
// failed (re)dial attempts. It mirrors the relay's nextBackoff so the platform
// has one backoff shape (relay.go). Reconnect backoff is NOT a confirm-at-build
// knob (epics.md:399) — a documented default is allowed.
type backoffPolicy struct {
	base time.Duration
	max  time.Duration
}

func (p backoffPolicy) next(cur time.Duration) time.Duration {
	n := cur * 2
	if n > p.max {
		n = p.max
	}
	if n < p.base {
		n = p.base
	}
	return n
}

// supervisor keeps a broker connection alive by re-dialing on loss — the
// in-process recovery amqp091-go does NOT provide (unlike the Java client). It
// reports each connected<->lost transition through onState exactly once per
// change, so a caller (e.g. the Leaderboard's stale flag) can react. A single
// goroutine (Run) drives it, so the connected field needs no lock.
type supervisor struct {
	connect connectFunc
	backoff backoffPolicy
	onState func(connected bool)
	log     *slog.Logger
	after   func(d time.Duration) <-chan time.Time // injectable timer (defaults to time.After)

	connected bool
}

func newSupervisor(connect connectFunc, backoff backoffPolicy, onState func(bool), log *slog.Logger) *supervisor {
	return &supervisor{connect: connect, backoff: backoff, onState: onState, log: log, after: time.After}
}

// setState reports a transition exactly once (a no-op when unchanged), so the
// onState callback never sees a spurious repeat.
func (s *supervisor) setState(connected bool) {
	if s.connected == connected {
		return
	}
	s.connected = connected
	if s.onState != nil {
		s.onState(connected)
	}
}

// Run maintains the connection until ctx is cancelled. On a failed dial it backs
// off (capped exponential) and retries; on a healthy connection it blocks until
// the connection drops, then reconnects (resetting the backoff). It never returns
// except on ctx cancellation, so it survives an arbitrarily long bus outage.
func (s *supervisor) Run(ctx context.Context) {
	backoff := s.backoff.base
	for {
		if ctx.Err() != nil {
			return
		}
		closed, err := s.connect(ctx)
		if err != nil {
			s.setState(false)
			s.log.Warn("bus connect failed; will retry", "error", err.Error(), "retryInMs", backoff.Milliseconds())
			select {
			case <-ctx.Done():
				return
			case <-s.after(backoff):
			}
			backoff = s.backoff.next(backoff)
			continue
		}
		backoff = s.backoff.base
		s.setState(true)
		s.log.Info("bus connected")
		select {
		case <-ctx.Done():
			return
		case <-closed:
			s.setState(false)
			s.log.Warn("bus connection lost; reconnecting")
		}
	}
}
