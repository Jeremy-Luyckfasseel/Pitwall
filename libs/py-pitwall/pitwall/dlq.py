"""The pure, I/O-free consumer-side poison-message escalation policy (mirrors
libs/go-pitwall/dlq/dlq.go): how many processing attempts a message gets before
terminal parking, and the exponential backoff between retry hops. A consumer turns a
Decision into a republish to the retry queue or the parking queue (see
pitwall.messaging retry_to_dlx/park_to_dlx). Values are pinned per Q&A Round 27 (5
attempts · 1 s base · x2 · 60 s ceiling) and supplied via env config — never invented
here.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class Policy:
    max_attempts: int  # total processing attempts (the live delivery + retries) before parking; >= 1
    base_ms: int  # first retry-hop delay in milliseconds; > 0
    multiplier: int  # exponential growth factor applied per hop; >= 1
    max_ms: int  # ceiling on any single hop's delay; >= base_ms


@dataclass(frozen=True)
class Decision:
    park: bool
    delay_ms: int = 0
    next_retries: int = 0


def next_retry(prior_retries: int, policy: Policy) -> Decision:
    """Decides what happens after a processing failure. prior_retries is how many
    times this message has ALREADY been retried (0 on the first failure of a
    freshly-delivered message). The live delivery counts as attempt 1, so the current
    failure is attempt prior_retries+1; once that reaches max_attempts the message is
    parked (no further requeue). Otherwise it is retried after an exponential backoff
    delay = min(base_ms * multiplier^prior_retries, max_ms). Pure and I/O-free."""
    attempts_made = prior_retries + 1
    if attempts_made >= policy.max_attempts:
        return Decision(park=True)
    return Decision(park=False, delay_ms=_backoff_ms(prior_retries, policy), next_retries=prior_retries + 1)


def _backoff_ms(hop: int, policy: Policy) -> int:
    """Computes base_ms * multiplier^hop, clamped to max_ms. Multiplies iteratively and
    clamps as it goes so a large hop count can never overflow."""
    delay = policy.base_ms
    for _ in range(hop):
        delay *= policy.multiplier
        if delay >= policy.max_ms:
            return policy.max_ms
    return min(delay, policy.max_ms)
