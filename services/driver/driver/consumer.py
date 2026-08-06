"""Driver's consumer (Story 3.2): the minimal-profile safety net. Consumes
lap.recorded (timing.events) and identity.resolved (identity.events) off ONE queue
(Task 2's multi-binding), validates on consume, and -- inside one transaction --
dedupes on the envelope id (idempotent inbox), creates a minimal racing profile if
(and only if) the masterId isn't local yet, and atomically enqueues
driver.profile_updated when a profile is actually created. Mirrors
services/identity/internal/consumer/consumer.go's Handler.Process shape (validate ->
decode -> dispatch -> transactional dedupe-and-write -> ack/retry/park).
"""

from __future__ import annotations

import sqlite3
from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime

from driver.domain.profile import MinimalProfile, build_profile_updated
from driver.persistence.profiles import insert_minimal_profile, profile_exists
from pitwall.dlq import Policy, next_retry
from pitwall.envelope import decode_incoming, format_wire_time
from pitwall.messaging import Delivery
from pitwall.persistence import inbox_has_seen, inbox_mark_seen, outbox_enqueue, within_tx

LAP_RECORDED_TYPE = "lap.recorded"
IDENTITY_RESOLVED_TYPE = "identity.resolved"

_HANDLED_TYPES = (LAP_RECORDED_TYPE, IDENTITY_RESOLVED_TYPE)


@dataclass(frozen=True)
class ProcessResult:
    duplicate: bool  # the envelope id was already processed (inbox dedupe no-op)
    created: bool  # a profile was newly created (never true when duplicate is true)


def ensure_minimal_profile(
    conn: sqlite3.Connection,
    source: str,
    master_id: str,
    envelope_id: str,
    event_type: str,
    correlation_id: str,
    at: str,
) -> ProcessResult:
    """The transactional core (FR12/FR13): inbox-dedupe -> create-if-absent ->
    enqueue driver.profile_updated ONLY on a genuine creation -> inbox-mark, all in
    one transaction. When the profile already exists this is a pure no-op besides
    the inbox mark -- AC3's precedence rule, enforced by
    driver.persistence.profiles.insert_minimal_profile's own construction."""
    with within_tx(conn):
        if inbox_has_seen(conn, envelope_id):
            return ProcessResult(duplicate=True, created=False)

        created = not profile_exists(conn, master_id)
        if created:
            insert_minimal_profile(conn, master_id, at)
            env = build_profile_updated(
                source=source,
                correlation_id=correlation_id,
                causation_id=envelope_id,
                occurred_at=at,
                profile=MinimalProfile(master_id=master_id),
            )
            payload = env.model_dump_json(by_alias=True, exclude_none=False).encode("utf-8")
            outbox_enqueue(conn, env.id, env.type, payload, created_at=at)

        inbox_mark_seen(conn, envelope_id, event_type, at)

    return ProcessResult(duplicate=False, created=created)


@dataclass
class Handler:
    """Processes one delivery at a time. validate/retry/park are injected so this
    class is unit-testable with fakes (no broker); conn is a real (or in-memory)
    sqlite3.Connection since the transactional core needs real transaction semantics."""

    validate: Callable[[bytes], None]  # validate-on-consume; raises on invalid
    conn: sqlite3.Connection
    source: str
    policy: Policy
    log: object
    retry: Callable[[bytes, int, int], None] | None = None
    park: Callable[[bytes, str], None] | None = None
    notify: Callable[[], None] | None = None  # kick the relay after a fresh outbox row
    now: Callable[[], datetime] = datetime.now

    def process(self, delivery: Delivery) -> None:
        body = delivery.body

        try:
            self.validate(body)
        except Exception as e:
            self.log.error("rejecting invalid message (failed /contract validation on consume)", error=str(e))
            self._park(delivery, body, "contract-invalid")
            return

        try:
            env = decode_incoming(body)
        except Exception as e:
            self.log.error("rejecting undecodable envelope", error=str(e))
            self._park(delivery, body, "undecodable-envelope")
            return

        if env.type not in _HANDLED_TYPES:
            # Tolerant reader: a valid type Driver does not handle here.
            self.log.debug("ignoring unhandled event type", type=env.type, eventId=env.id)
            delivery.ack()
            return

        master_id = env.data.get("masterId") if isinstance(env.data, dict) else None
        if not master_id:
            self.log.error("rejecting message with missing masterId", eventId=env.id, type=env.type)
            self._park(delivery, body, "missing-master-id")
            return

        try:
            result = ensure_minimal_profile(
                self.conn,
                source=self.source,
                master_id=master_id,
                envelope_id=env.id,
                event_type=env.type,
                correlation_id=env.correlation_id,
                at=format_wire_time(self.now()),
            )
        except Exception as e:
            self._retry_or_park(delivery, body, env.id, env.correlation_id, e)
            return

        delivery.ack()
        if result.duplicate:
            self.log.debug("duplicate envelope ignored (idempotent inbox)", eventId=env.id, type=env.type)
            return
        if not result.created:
            # AC3: the masterId already has a local profile -- this IS "the conflict",
            # and this line is what makes it "logged" (the write itself never happens,
            # by construction in insert_minimal_profile).
            self.log.info(
                "profile already local, skipped (Driver's write wins)",
                masterId=master_id, eventId=env.id, type=env.type, correlationId=env.correlation_id,
            )
            return
        if self.notify is not None:
            self.notify()
        self.log.info(
            "minimal racing profile created", masterId=master_id, eventId=env.id, type=env.type,
            correlationId=env.correlation_id,
        )

    def _retry_or_park(self, delivery: Delivery, body: bytes, event_id: str, correlation_id: str, cause: Exception) -> None:
        decision = next_retry(delivery.retry_count, self.policy)
        if decision.park:
            self.log.error(
                "processing kept failing; parking after exhausting retries",
                error=str(cause), eventId=event_id, maxAttempts=self.policy.max_attempts,
            )
            self._park(delivery, body, "retries-exhausted")
            return
        if self.retry is None:
            delivery.nack(requeue=True)
            return
        try:
            self.retry(body, decision.delay_ms, decision.next_retries)
        except Exception as e:
            self.log.error("failed to schedule DLQ retry; requeueing", error=str(e), eventId=event_id)
            delivery.nack(requeue=True)
            return
        self.log.warn(
            "processing failed; scheduled DLQ retry", error=str(cause), eventId=event_id,
            retryInMs=decision.delay_ms, attempt=decision.next_retries, correlationId=correlation_id,
        )
        delivery.ack()

    def _park(self, delivery: Delivery, body: bytes, reason: str) -> None:
        if self.park is None:
            delivery.nack(requeue=False)
            return
        try:
            self.park(body, reason)
        except Exception as e:
            self.log.error("failed to park message; requeueing", error=str(e), reason=reason)
            delivery.nack(requeue=True)
            return
        self.log.error("message parked (quarantined); not retried", alert="message_parked", reason=reason)
        delivery.ack()
