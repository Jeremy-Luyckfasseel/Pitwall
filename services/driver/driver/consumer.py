"""Driver's consumer. Consumes lap.recorded (timing.events), session.ended
(timing.events), and identity.resolved (identity.events) off ONE queue (multi-binding,
Story 3.2 Task 2), validates on consume, and -- inside one transaction per envelope --
dedupes on the envelope id (idempotent inbox) and applies the per-type effect:

  - identity.resolved -> minimal-profile safety net only (Story 3.2, FR12/FR13).
  - lap.recorded       -> safety net + append the lap to full history (Story 3.3, FR9).
  - session.ended      -> for EACH summary row: safety net + store the per-session
                          summary + enqueue one driver.history_appended (Story 3.3,
                          FR10 / Q&A Round 36 Q36.2/Q36.3).

The safety net create-if-absent runs for every masterId seen (a lap.recorded masterId
or each session.ended summary-row masterId, Q36.3), so no lap/summary row can reference
a masterId with no driver_profiles row. Mirrors
services/identity/internal/consumer/consumer.go's Handler.Process shape (validate ->
decode -> dispatch -> transactional dedupe-and-write -> ack/retry/park).
"""

from __future__ import annotations

import sqlite3
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import datetime

from driver.domain.history import build_history_appended
from driver.domain.profile import MinimalProfile, build_profile_updated
from driver.persistence.history import append_lap, upsert_session_summary
from driver.persistence.profiles import insert_minimal_profile
from pitwall.dlq import Policy, next_retry
from pitwall.envelope import decode_incoming, format_wire_time
from pitwall.messaging import Delivery
from pitwall.persistence import inbox_has_seen, inbox_mark_seen, outbox_enqueue, within_tx

LAP_RECORDED_TYPE = "lap.recorded"
IDENTITY_RESOLVED_TYPE = "identity.resolved"
SESSION_ENDED_TYPE = "session.ended"

_HANDLED_TYPES = (LAP_RECORDED_TYPE, IDENTITY_RESOLVED_TYPE, SESSION_ENDED_TYPE)


class _PayloadError(Exception):
    """A structurally-malformed payload Driver relies on (missing a field the
    transactional core needs). Carries the park reason. Raised during parsing, BEFORE
    any transaction opens, so a park never leaves a partial write behind."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Parsed payloads (extracted + shape-checked before the transaction opens).
# ---------------------------------------------------------------------------
@dataclass(frozen=True)
class _Lap:
    master_id: str
    session_id: str
    lap_number: int
    lap_time_ms: int
    at: str


@dataclass(frozen=True)
class _SummaryRow:
    master_id: str
    best_lap_ms: int | None
    lap_count: int | None


@dataclass(frozen=True)
class _SessionEnded:
    session_id: str
    rows: list[_SummaryRow]


# ---------------------------------------------------------------------------
# Results of the transactional cores.
# ---------------------------------------------------------------------------
@dataclass(frozen=True)
class ProfileResult:
    duplicate: bool
    profile_created: bool

    @property
    def enqueued(self) -> bool:
        return self.profile_created


@dataclass(frozen=True)
class LapResult:
    duplicate: bool
    profile_created: bool
    lap_appended: bool

    @property
    def enqueued(self) -> bool:
        return self.profile_created


@dataclass(frozen=True)
class SessionEndedResult:
    duplicate: bool
    profiles_created: int = 0
    histories_appended: int = 0

    @property
    def enqueued(self) -> bool:
        return self.histories_appended > 0 or self.profiles_created > 0


def _ensure_profile_within_tx(
    conn: sqlite3.Connection,
    source: str,
    master_id: str,
    causation_id: str,
    correlation_id: str,
    at: str,
) -> bool:
    """The minimal-profile safety net (FR12/FR13), running inside an already-open
    transaction. Creates an all-null profile row if (and only if) master_id isn't local
    yet -- never overwrites (insert_minimal_profile is create-only by construction) --
    and, ONLY on a genuine creation, atomically enqueues driver.profile_updated. Returns
    whether a profile was actually created."""
    created = insert_minimal_profile(conn, master_id, at)
    if created:
        env = build_profile_updated(
            source=source,
            correlation_id=correlation_id,
            causation_id=causation_id,
            occurred_at=at,
            profile=MinimalProfile(master_id=master_id),
        )
        payload = env.model_dump_json(by_alias=True, exclude_none=False).encode("utf-8")
        outbox_enqueue(conn, env.id, env.type, payload, created_at=at)
    return created


def process_identity_resolved(
    conn: sqlite3.Connection,
    source: str,
    envelope_id: str,
    event_type: str,
    correlation_id: str,
    master_id: str,
    at: str,
) -> ProfileResult:
    """identity.resolved: inbox-dedupe -> safety net -> inbox-mark, one transaction."""
    with within_tx(conn):
        if inbox_has_seen(conn, envelope_id):
            return ProfileResult(duplicate=True, profile_created=False)
        created = _ensure_profile_within_tx(conn, source, master_id, envelope_id, correlation_id, at)
        inbox_mark_seen(conn, envelope_id, event_type, at)
    return ProfileResult(duplicate=False, profile_created=created)


def process_lap_recorded(
    conn: sqlite3.Connection,
    source: str,
    envelope_id: str,
    event_type: str,
    correlation_id: str,
    lap: _Lap,
    at: str,
) -> LapResult:
    """lap.recorded (AC1): inbox-dedupe -> safety net + append the lap to history ->
    inbox-mark, ALL in one transaction sharing the single inbox mark for this envelope
    (so a redelivery is a full no-op -- the lap is never double-counted, FR9/NFR3)."""
    with within_tx(conn):
        if inbox_has_seen(conn, envelope_id):
            return LapResult(duplicate=True, profile_created=False, lap_appended=False)
        created = _ensure_profile_within_tx(conn, source, lap.master_id, envelope_id, correlation_id, at)
        appended = append_lap(
            conn, lap.master_id, lap.session_id, lap.lap_number, lap.lap_time_ms, lap.at, envelope_id, at
        )
        inbox_mark_seen(conn, envelope_id, event_type, at)
    return LapResult(duplicate=False, profile_created=created, lap_appended=appended)


def process_session_ended(
    conn: sqlite3.Connection,
    source: str,
    envelope_id: str,
    event_type: str,
    correlation_id: str,
    session: _SessionEnded,
    at: str,
) -> SessionEndedResult:
    """session.ended (AC2/AC3): inbox-dedupe -> for EACH summary row: safety net +
    store the summary + enqueue one driver.history_appended -> a SINGLE inbox-mark for
    the whole envelope, all in one transaction. A redelivery is a full no-op (no second
    history_appended, no duplicate summary rows)."""
    with within_tx(conn):
        if inbox_has_seen(conn, envelope_id):
            return SessionEndedResult(duplicate=True)
        profiles_created = 0
        histories_appended = 0
        for row in session.rows:
            if _ensure_profile_within_tx(conn, source, row.master_id, envelope_id, correlation_id, at):
                profiles_created += 1
            upsert_session_summary(conn, row.master_id, session.session_id, row.best_lap_ms, row.lap_count, at)
            env = build_history_appended(
                source=source,
                correlation_id=correlation_id,
                causation_id=envelope_id,
                occurred_at=at,
                master_id=row.master_id,
                session_id=session.session_id,
                best_lap_ms=row.best_lap_ms,
                lap_count=row.lap_count,
            )
            payload = env.model_dump_json(by_alias=True, exclude_none=False).encode("utf-8")
            outbox_enqueue(conn, env.id, env.type, payload, created_at=at)
            histories_appended += 1
        inbox_mark_seen(conn, envelope_id, event_type, at)
    return SessionEndedResult(
        duplicate=False, profiles_created=profiles_created, histories_appended=histories_appended
    )


# ---------------------------------------------------------------------------
# Payload parsing (pure, no I/O; raises _PayloadError -> park before any write).
# ---------------------------------------------------------------------------
def _require_str(data: dict, key: str, reason: str) -> str:
    value = data.get(key)
    if not isinstance(value, str) or not value:
        raise _PayloadError(reason)
    return value


def _require_int(data: dict, key: str, reason: str) -> int:
    value = data.get(key)
    # bool is an int subclass -- reject it explicitly so a stray true/false can't pose
    # as a lap number / time.
    if not isinstance(value, int) or isinstance(value, bool):
        raise _PayloadError(reason)
    return value


def _optional_int(data: dict, key: str, reason: str) -> int | None:
    value = data.get(key)
    if value is None:
        return None
    if not isinstance(value, int) or isinstance(value, bool):
        raise _PayloadError(reason)
    return value


def _parse_lap(data: dict) -> _Lap:
    return _Lap(
        master_id=_require_str(data, "masterId", "missing-master-id"),
        session_id=_require_str(data, "sessionId", "malformed-lap"),
        lap_number=_require_int(data, "lapNumber", "malformed-lap"),
        lap_time_ms=_require_int(data, "lapTimeMs", "malformed-lap"),
        at=_require_str(data, "at", "malformed-lap"),
    )


def _parse_session_ended(data: dict) -> _SessionEnded:
    session_id = _require_str(data, "sessionId", "malformed-session-ended")
    raw = data.get("summary")
    if not isinstance(raw, list):
        raise _PayloadError("malformed-session-ended")
    rows: list[_SummaryRow] = []
    for item in raw:
        if not isinstance(item, dict):
            raise _PayloadError("malformed-summary-row")
        rows.append(
            _SummaryRow(
                master_id=_require_str(item, "masterId", "malformed-summary-row"),
                best_lap_ms=_optional_int(item, "bestLapMs", "malformed-summary-row"),
                lap_count=_optional_int(item, "lapCount", "malformed-summary-row"),
            )
        )
    return _SessionEnded(session_id=session_id, rows=rows)


@dataclass
class Handler:
    """Processes one delivery at a time. validate/retry/park/notify are injected so this
    class is unit-testable with fakes (no broker); conn is a real (or in-memory)
    sqlite3.Connection since the transactional cores need real transaction semantics."""

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
            self._ack(delivery)
            return

        data = env.data if isinstance(env.data, dict) else {}
        at = format_wire_time(self.now())

        # Parse + shape-check the payload BEFORE opening any transaction, so a malformed
        # message parks cleanly with no partial write.
        try:
            parsed = self._parse(env.type, data)
        except _PayloadError as e:
            self.log.error("rejecting message with malformed payload", reason=e.reason, eventId=env.id, type=env.type)
            self._park(delivery, body, e.reason)
            return

        try:
            result = self._apply(env, parsed, at)
        except Exception as e:
            self._retry_or_park(delivery, body, env.id, env.correlation_id, e)
            return

        # Success-path tail: ack + logging + the relay kick. None of it may crash this
        # daemon thread -- an uncaught exception here would silently and permanently kill
        # the consumer (nothing else notices; the heartbeat keeps beating). Each step is
        # guarded independently rather than wrapping the whole tail in one try/except.
        self._ack(delivery)
        self._log_and_notify(env, result)

    def _parse(self, event_type: str, data: dict):
        if event_type == LAP_RECORDED_TYPE:
            return _parse_lap(data)
        if event_type == SESSION_ENDED_TYPE:
            return _parse_session_ended(data)
        # identity.resolved: only masterId is needed for the safety net.
        return _require_str(data, "masterId", "missing-master-id")

    def _apply(self, env, parsed, at):
        if env.type == LAP_RECORDED_TYPE:
            return process_lap_recorded(
                self.conn, self.source, env.id, env.type, env.correlation_id, parsed, at
            )
        if env.type == SESSION_ENDED_TYPE:
            return process_session_ended(
                self.conn, self.source, env.id, env.type, env.correlation_id, parsed, at
            )
        return process_identity_resolved(
            self.conn, self.source, env.id, env.type, env.correlation_id, parsed, at
        )

    def _log_and_notify(self, env, result) -> None:
        if result.duplicate:
            self.log.debug("duplicate envelope ignored (idempotent inbox)", eventId=env.id, type=env.type)
            return

        if isinstance(result, SessionEndedResult):
            self.log.info(
                "session summaries stored and history appended",
                eventId=env.id, type=env.type, correlationId=env.correlation_id,
                summariesStored=result.histories_appended, profilesCreated=result.profiles_created,
            )
        elif isinstance(result, LapResult):
            self.log.info(
                "lap appended to history", eventId=env.id, type=env.type,
                correlationId=env.correlation_id, profileCreated=result.profile_created,
                lapAppended=result.lap_appended,
            )
        elif not result.profile_created:
            # identity.resolved for an already-local masterId: AC3's "conflict",
            # logged. (The write itself never happens, by construction.)
            master_id = env.data.get("masterId") if isinstance(env.data, dict) else None
            self.log.info(
                "profile already local, skipped (Driver's write wins)",
                masterId=master_id, eventId=env.id, type=env.type, correlationId=env.correlation_id,
            )
        else:
            master_id = env.data.get("masterId") if isinstance(env.data, dict) else None
            self.log.info(
                "minimal racing profile created", masterId=master_id, eventId=env.id, type=env.type,
                correlationId=env.correlation_id,
            )

        if result.enqueued and self.notify is not None:
            try:
                self.notify()
            except Exception as e:
                self.log.error("relay notify failed (non-fatal, poll interval is the backstop)", error=str(e))

    def _ack(self, delivery: Delivery) -> None:
        try:
            delivery.ack()
        except Exception as e:
            self.log.error("failed to ack delivery", error=str(e))

    def _nack(self, delivery: Delivery, requeue: bool) -> None:
        try:
            delivery.nack(requeue=requeue)
        except Exception as e:
            self.log.error("failed to nack delivery", error=str(e), requeue=requeue)

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
            self._nack(delivery, requeue=True)
            return
        try:
            self.retry(body, decision.delay_ms, decision.next_retries)
        except Exception as e:
            self.log.error("failed to schedule DLQ retry; requeueing", error=str(e), eventId=event_id)
            self._nack(delivery, requeue=True)
            return
        self.log.warn(
            "processing failed; scheduled DLQ retry", error=str(cause), eventId=event_id,
            retryInMs=decision.delay_ms, attempt=decision.next_retries, correlationId=correlation_id,
        )
        self._ack(delivery)

    def _park(self, delivery: Delivery, body: bytes, reason: str) -> None:
        if self.park is None:
            self._nack(delivery, requeue=False)
            return
        try:
            self.park(body, reason)
        except Exception as e:
            self.log.error("failed to park message; requeueing", error=str(e), reason=reason)
            self._nack(delivery, requeue=True)
            return
        self.log.error("message parked (quarantined); not retried", alert="message_parked", reason=reason)
        self._ack(delivery)
