"""Fake-based unit tests for driver.consumer.Handler (Story 3.2 Task 5). No broker: a
FakeDelivery stands in for pitwall.messaging.Delivery, and validate/retry/park are
plain recording callables. conn is a real (Alembic-migrated) SQLite connection since
the transactional core needs real transaction semantics -- mirrors
libs/py-pitwall/tests/test_erasure.py's approach (real SQLite, fake broker seams).
"""

import json
from dataclasses import dataclass, field
from datetime import UTC, datetime

import pytest
from driver.consumer import IDENTITY_RESOLVED_TYPE, LAP_RECORDED_TYPE, Handler
from pitwall.dlq import Policy
from pitwall.persistence import open_db, outbox_fetch_pending

MASTER_ID = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
CORRELATION_ID = "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55"
NOW = datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC)


@dataclass
class FakeDelivery:
    body: bytes
    retry_count: int = 0
    acked: bool = False
    nacked: bool | None = None  # None = not nacked; else the requeue flag passed

    def ack(self) -> None:
        self.acked = True

    def nack(self, requeue: bool = False) -> None:
        self.nacked = requeue


@dataclass
class FakeLog:
    lines: list = field(default_factory=list)

    def _rec(self, level, message, **fields):
        self.lines.append((level, message, fields))

    def debug(self, message, **fields):
        self._rec("debug", message, **fields)

    def info(self, message, **fields):
        self._rec("info", message, **fields)

    def warn(self, message, **fields):
        self._rec("warn", message, **fields)

    def error(self, message, **fields):
        self._rec("error", message, **fields)

    def has(self, level, needle):
        return any(level == lvl and needle in msg for lvl, msg, _ in self.lines)


def _envelope_bytes(envelope_id: str, event_type: str, master_id: str, correlation_id: str = CORRELATION_ID) -> bytes:
    return json.dumps(
        {
            "id": envelope_id,
            "type": event_type,
            "source": "timing" if event_type == LAP_RECORDED_TYPE else "identity",
            "schemaVersion": 1,
            "envelopeVersion": 1,
            "occurredAt": "2026-06-02T14:03:21.512Z",
            "correlationId": correlation_id,
            "causationId": None,
            "data": {"masterId": master_id},
        }
    ).encode("utf-8")


@pytest.fixture()
def conn(migrated_db_path):
    c = open_db(migrated_db_path)
    yield c
    c.close()


def _handler(conn, log, *, retry=None, park=None, notify=None) -> Handler:
    return Handler(
        validate=lambda body: None,
        conn=conn,
        source="driver",
        policy=Policy(max_attempts=3, base_ms=100, multiplier=2, max_ms=1000),
        log=log,
        retry=retry,
        park=park,
        notify=notify,
        now=lambda: NOW,
    )


def test_lap_recorded_for_unknown_master_id_creates_profile_and_enqueues_profile_updated(conn):
    log = FakeLog()
    notified = []
    handler = _handler(conn, log, notify=lambda: notified.append(1))
    delivery = FakeDelivery(body=_envelope_bytes("00000000-0000-0000-0000-000000000001", LAP_RECORDED_TYPE, MASTER_ID))

    handler.process(delivery)

    assert delivery.acked is True
    assert delivery.nacked is None
    row = conn.execute("SELECT master_id FROM driver_profiles WHERE master_id = ?", (MASTER_ID,)).fetchone()
    assert row is not None
    outbox = outbox_fetch_pending(conn, 10)
    assert len(outbox) == 1
    assert outbox[0].routing_key == "driver.profile_updated"
    assert notified == [1]
    assert log.has("info", "minimal racing profile created")


def test_identity_resolved_for_unknown_master_id_also_creates_profile(conn):
    """Both trigger types share the same safety net (AC2)."""
    log = FakeLog()
    handler = _handler(conn, log)
    delivery = FakeDelivery(body=_envelope_bytes("00000000-0000-0000-0000-000000000002", IDENTITY_RESOLVED_TYPE, MASTER_ID))

    handler.process(delivery)

    assert delivery.acked is True
    row = conn.execute("SELECT master_id FROM driver_profiles WHERE master_id = ?", (MASTER_ID,)).fetchone()
    assert row is not None


def test_redelivery_of_the_same_envelope_id_is_a_no_op(conn):
    log = FakeLog()
    handler = _handler(conn, log)
    first = FakeDelivery(body=_envelope_bytes("00000000-0000-0000-0000-000000000003", LAP_RECORDED_TYPE, MASTER_ID))
    handler.process(first)
    assert len(outbox_fetch_pending(conn, 10)) == 1

    second = FakeDelivery(body=_envelope_bytes("00000000-0000-0000-0000-000000000003", LAP_RECORDED_TYPE, MASTER_ID))
    handler.process(second)

    assert second.acked is True
    assert len(outbox_fetch_pending(conn, 10)) == 1  # no second publish
    assert log.has("debug", "duplicate envelope ignored")


def test_second_distinct_trigger_for_an_already_local_master_id_is_never_overwritten(conn):
    """AC3: a SECOND, distinct envelope (different id) for a masterId that already
    has a local profile must not touch the row and must not publish a second
    driver.profile_updated -- and the skip must be logged (the 'conflict... logged'
    case)."""
    log = FakeLog()
    handler = _handler(conn, log)
    handler.process(FakeDelivery(body=_envelope_bytes("00000000-0000-0000-0000-000000000004", LAP_RECORDED_TYPE, MASTER_ID)))
    assert len(outbox_fetch_pending(conn, 10)) == 1

    second_delivery = FakeDelivery(body=_envelope_bytes("00000000-0000-0000-0000-000000000005", IDENTITY_RESOLVED_TYPE, MASTER_ID))
    handler.process(second_delivery)

    assert second_delivery.acked is True
    assert len(outbox_fetch_pending(conn, 10)) == 1  # still just the one
    assert log.has("info", "profile already local, skipped")


def test_invalid_message_is_parked_immediately_not_retried(conn):
    log = FakeLog()
    parked = []
    handler = Handler(
        validate=lambda body: (_ for _ in ()).throw(ValueError("bad schema")),
        conn=conn,
        source="driver",
        policy=Policy(max_attempts=3, base_ms=100, multiplier=2, max_ms=1000),
        log=log,
        park=lambda body, reason: parked.append(reason),
        now=lambda: NOW,
    )
    delivery = FakeDelivery(body=b"garbage")

    handler.process(delivery)

    assert parked == ["contract-invalid"]
    assert delivery.acked is True  # ack only after the park publish succeeds


def test_undecodable_envelope_is_parked_immediately_not_retried(conn):
    """A message that passes /contract validation (or whose validation is skipped/
    stubbed) but is not parseable as a PitwallMessageEnvelope -- distinct from the
    schema-invalid branch above, parks with a different reason."""
    log = FakeLog()
    parked = []
    handler = Handler(
        validate=lambda body: None,  # passes validation...
        conn=conn,
        source="driver",
        policy=Policy(max_attempts=3, base_ms=100, multiplier=2, max_ms=1000),
        log=log,
        park=lambda body, reason: parked.append(reason),
        now=lambda: NOW,
    )
    delivery = FakeDelivery(body=b"not json at all")  # ...but decode_incoming raises

    handler.process(delivery)

    assert parked == ["undecodable-envelope"]
    assert delivery.acked is True  # ack only after the park publish succeeds


def test_processing_failure_retries_then_parks_at_the_cap(conn):
    log = FakeLog()
    retried = []
    parked = []

    handler = Handler(
        validate=lambda body: None,
        conn=None,  # forces ensure_minimal_profile to raise (no connection)
        source="driver",
        policy=Policy(max_attempts=2, base_ms=100, multiplier=2, max_ms=1000),
        log=log,
        retry=lambda body, delay_ms, next_retries: retried.append((delay_ms, next_retries)),
        park=lambda body, reason: parked.append(reason),
        now=lambda: NOW,
    )

    # First failure (retry_count=0): below max_attempts=2, so it retries.
    first = FakeDelivery(body=_envelope_bytes("00000000-0000-0000-0000-000000000006", LAP_RECORDED_TYPE, MASTER_ID), retry_count=0)
    handler.process(first)
    assert retried == [(100, 1)]
    assert parked == []
    assert first.acked is True
    # Confirms the failure is specifically conn=None's AttributeError from within_tx's
    # `conn.execute("BEGIN")` -- not some unrelated bug elsewhere in ensure_minimal_profile
    # that happens to also raise.
    warn_fields = next(fields for level, _, fields in log.lines if level == "warn")
    assert "NoneType" in warn_fields["error"] and "execute" in warn_fields["error"]

    # Second failure (retry_count=1, i.e. attempt 2 of max_attempts=2): parks.
    second = FakeDelivery(body=_envelope_bytes("00000000-0000-0000-0000-000000000006", LAP_RECORDED_TYPE, MASTER_ID), retry_count=1)
    handler.process(second)
    assert parked == ["retries-exhausted"]
    assert second.acked is True
    error_fields = next(fields for level, _, fields in log.lines if level == "error" and fields.get("maxAttempts"))
    assert "NoneType" in error_fields["error"] and "execute" in error_fields["error"]


def test_ack_failure_on_the_success_path_is_logged_not_raised(conn):
    """A crashed daemon consumer thread is a silent, unrecoverable outage of the
    whole safety net (nothing else signals it -- heartbeat keeps beating). Mirrors
    Identity's Go handler, which logs an Ack() failure rather than letting it
    propagate."""

    class RaisingAckDelivery(FakeDelivery):
        def ack(self) -> None:
            raise RuntimeError("channel gone")

    log = FakeLog()
    handler = _handler(conn, log)
    delivery = RaisingAckDelivery(
        body=_envelope_bytes("00000000-0000-0000-0000-00000000000a", LAP_RECORDED_TYPE, MASTER_ID)
    )

    handler.process(delivery)  # must not raise

    assert log.has("error", "ack")
    # The profile was still created -- only the post-processing ack/notify tail failed.
    row = conn.execute("SELECT master_id FROM driver_profiles WHERE master_id = ?", (MASTER_ID,)).fetchone()
    assert row is not None


def test_nack_failure_is_logged_not_raised(conn):
    """The twin of test_ack_failure_on_the_success_path_is_logged_not_raised: _nack is
    the same guarded log-and-continue helper as _ack (used by _park's own fallback when
    no park callback is configured), and must not crash the daemon consumer thread
    either."""

    class RaisingNackDelivery(FakeDelivery):
        def nack(self, requeue: bool = False) -> None:
            raise RuntimeError("channel gone")

    log = FakeLog()
    handler = Handler(
        validate=lambda body: (_ for _ in ()).throw(ValueError("bad schema")),
        conn=conn,
        source="driver",
        policy=Policy(max_attempts=3, base_ms=100, multiplier=2, max_ms=1000),
        log=log,
        park=None,  # forces _park's fallback: self._nack(delivery, requeue=False)
        now=lambda: NOW,
    )
    delivery = RaisingNackDelivery(body=b"garbage")

    handler.process(delivery)  # must not raise

    assert log.has("error", "nack")
    assert delivery.acked is False
    assert delivery.nacked is None  # RaisingNackDelivery never actually records a nack


def test_unhandled_event_type_is_acked_and_ignored(conn):
    log = FakeLog()
    handler = _handler(conn, log)
    delivery = FakeDelivery(body=_envelope_bytes("00000000-0000-0000-0000-000000000007", "control.heartbeat", MASTER_ID))

    handler.process(delivery)

    assert delivery.acked is True
    assert conn.execute("SELECT COUNT(*) FROM driver_profiles").fetchone()[0] == 0
