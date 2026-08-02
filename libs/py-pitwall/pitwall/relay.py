"""The outbox publisher loop -- the reliability spine. Bridges the durable outbox
(pitwall.persistence) and the bus (pitwall.messaging): scans pending rows oldest-first,
validates each against /contract, publishes to the service's own exchange, and marks a
row sent ONLY after publish succeeds.

Failure handling is deliberate and split:
  - validation failure -> terminal: quarantine, never publish, never retry
  - publish failure     -> transient: keep pending, back off, retry forever

A service's own valid events are never poison, so a transient failure is never parked
or dropped; only an event that cannot be validated is quarantined (a producer-side
quarantine, distinct from the consumer-side RabbitMQ DLQ/parking). Mirrors
libs/go-pitwall/relay/relay.go, using a stop+kick threading.Event pair instead of a
context + channel select (idiomatic Python) since it runs in its own thread alongside
the service's other loops. Mechanics only -- the events, exchange and schemas are the
service's.
"""

from __future__ import annotations

import threading
import time
from collections.abc import Callable
from typing import Any

from pitwall.persistence import OutboxRow

FetchPending = Callable[[int], list[OutboxRow]]
MarkSent = Callable[[str, str], None]
MarkQuarantined = Callable[[str, str], None]
RecordFailure = Callable[[str, str], None]
Validate = Callable[[bytes], None]  # raises to reject (never publish)
Publish = Callable[[str, bytes], None]  # raises on transient failure

_MAX_BACKOFF_S = 5.0
_KICK_POLL_S = 0.05  # how promptly a kick() is noticed by a blocked wait


class Relay:
    """Drains the outbox. Construct with the service's persistence callables (already
    bound to a connection dedicated to this relay's own thread -- SQLite connections are
    not shared across threads), a validator, and a publisher; run() blocks in the
    caller's thread until stop is set."""

    def __init__(
        self,
        fetch_pending: FetchPending,
        mark_sent: MarkSent,
        mark_quarantined: MarkQuarantined,
        record_failure: RecordFailure,
        validate: Validate,
        publish: Publish,
        interval_s: float,
        log: Any,
        batch: int = 100,
        now: Callable[[], str] | None = None,
    ):
        if interval_s <= 0:
            raise ValueError(f"Relay interval_s must be positive, got {interval_s}")
        self._fetch_pending = fetch_pending
        self._mark_sent = mark_sent
        self._mark_quarantined = mark_quarantined
        self._record_failure = record_failure
        self._validate = validate
        self._publish = publish
        self._interval_s = interval_s
        self._log = log
        self._batch = batch
        self._now = now or _default_now
        self._kick_event = threading.Event()

    def kick(self) -> None:
        """Signals the relay to drain promptly (best-effort, non-blocking). The poll
        interval is the durable backstop if the kick is missed."""
        self._kick_event.set()

    def run(self, stop: threading.Event) -> None:
        """Drains immediately, then on every poll tick or kick, until stop is set.
        Backs off (capped exponential) while the broker is unreachable and resets to
        the base interval once a drain succeeds. Performs one final bounded drain
        (flush) before returning -- called from THIS method so it stays on the same
        thread as every other call this relay makes (see flush()'s docstring for why
        that matters: the fetch_pending/mark_sent/etc. callables are typically bound to
        a sqlite3.Connection, which is only usable from the thread that opened it)."""
        backoff = self._interval_s
        while True:
            ok = self._drain_once_safe()
            backoff = self._interval_s if ok else min(backoff * 2, _MAX_BACKOFF_S)
            if self._wait(stop, backoff):
                sent, remaining = self.flush()
                self._log.info("relay stopped", sent=sent, remaining=remaining)
                return

    def _wait(self, stop: threading.Event, timeout: float) -> bool:
        """Waits up to timeout for stop, waking early (returning False) on a kick.
        Returns True iff stop was set."""
        deadline = time.monotonic() + timeout
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return stop.is_set()
            if stop.wait(min(remaining, _KICK_POLL_S)):
                return True
            if self._kick_event.is_set():
                self._kick_event.clear()
                return False

    def _drain_once_safe(self) -> bool:
        """Runs drain_once, logging and treating any unexpected exception as a
        transient failure (backs off) rather than letting it kill the relay thread."""
        try:
            self.drain_once()
            return True
        except Exception as e:
            self._log.error("relay drain failed", error=str(e), errorType=type(e).__name__)
            return False

    def drain_once(self) -> int:
        """Processes one batch of pending rows. Returns the number marked sent.
        Validation failures are terminal and do not stop the drain -- they are
        quarantined and the drain continues with the next row. A publish failure stops
        the drain (the broker is likely down for the whole batch) and propagates so the
        caller can back off."""
        rows = self._fetch_pending(self._batch)
        sent = 0
        for row in rows:
            try:
                self._validate(row.payload)
            except Exception as e:
                self._log.error(
                    "quarantining outbox row: failed /contract validation",
                    id=row.id,
                    routingKey=row.routing_key,
                    error=str(e),
                )
                self._mark_quarantined(row.id, str(e))
                continue  # a quarantined row must not block healthy rows behind it

            try:
                self._publish(row.routing_key, row.payload)
            except Exception as e:
                self._log.warning(
                    "publish failed; row stays pending, will retry",
                    id=row.id,
                    routingKey=row.routing_key,
                    error=str(e),
                )
                self._record_failure(row.id, str(e))
                raise  # broker is likely down for the whole batch -- stop now, back off, retry

            self._mark_sent(row.id, self._now())
            sent += 1
        return sent

    def flush(self) -> tuple[int, int]:
        """A bounded best-effort drain for graceful shutdown. Attempts one batch and
        reports how many were sent and how many remain pending; whatever remains stays
        durably in the outbox for the next start (no loss either way).

        MUST be called from the same thread that will otherwise call drain_once()/run()
        -- fetch_pending/mark_sent/mark_quarantined/record_failure are ordinarily bound
        to a sqlite3.Connection, and Python's default check_same_thread=True makes such
        a connection unusable from any thread other than the one that opened it. run()
        already calls this itself on stop for exactly this reason; a caller on a
        DIFFERENT thread than the one running run() must not call flush() directly."""
        try:
            sent = self.drain_once()
        except Exception:
            sent = 0
        remaining = len(self._fetch_pending(self._batch))
        return sent, remaining


def _default_now() -> str:
    from pitwall.envelope import format_wire_time
    from datetime import datetime, UTC

    return format_wire_time(datetime.now(UTC))
