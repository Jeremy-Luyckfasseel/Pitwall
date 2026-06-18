package domain

// DLQPolicy is the consumer-side poison-message escalation policy (Story 1.9): how
// many processing attempts a message gets before terminal parking, and the
// exponential backoff between retry hops. Values are pinned in Q&A Round 27 (5
// attempts · 1 s base · ×2 · 60 s ceiling) and supplied via env config — never
// invented here.
//
// NB: this mirrors services/leaderboard/internal/domain/dlq.go verbatim — pure DLQ
// mechanics now shared by a SECOND Go consumer. It is a candidate for extraction into
// libs/go-pitwall (per the Story 2.1 "extract on the second consumer" philosophy);
// kept local for now to keep Story 2.2 scoped and leaderboard untouched. See
// _bmad-output/implementation-artifacts/deferred-work.md.
type DLQPolicy struct {
	MaxAttempts int // total processing attempts (the live delivery + retries) before parking; >= 1
	BaseMs      int // first retry-hop delay in milliseconds; > 0
	Multiplier  int // exponential growth factor applied per hop; >= 1
	MaxMs       int // ceiling on any single hop's delay; >= BaseMs
}

// Decision is the outcome of NextRetry: either retry after DelayMs (carrying the
// incremented NextRetries count onward), or Park the message terminally.
type Decision struct {
	Park        bool
	DelayMs     int
	NextRetries int
}

// NextRetry decides what happens after a processing failure. priorRetries is how many
// times this message has ALREADY been retried (0 on the first failure of a
// freshly-delivered message). The live delivery counts as attempt 1, so the current
// failure is attempt priorRetries+1; once that reaches MaxAttempts the message is
// parked (no further requeue). Otherwise it is retried after an exponential backoff
// delay = min(BaseMs · Multiplier^priorRetries, MaxMs). Pure and I/O-free.
func NextRetry(priorRetries int, p DLQPolicy) Decision {
	attemptsMade := priorRetries + 1
	if attemptsMade >= p.MaxAttempts {
		return Decision{Park: true}
	}
	return Decision{
		Park:        false,
		DelayMs:     backoffMs(priorRetries, p),
		NextRetries: priorRetries + 1,
	}
}

// backoffMs computes BaseMs · Multiplier^hop, clamped to MaxMs. It multiplies
// iteratively and clamps as it goes so a large hop count can never overflow.
func backoffMs(hop int, p DLQPolicy) int {
	delay := p.BaseMs
	for i := 0; i < hop; i++ {
		delay *= p.Multiplier
		if delay >= p.MaxMs {
			return p.MaxMs
		}
	}
	if delay > p.MaxMs {
		return p.MaxMs
	}
	return delay
}
