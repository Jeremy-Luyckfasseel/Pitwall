"""Real-broker + real-SQLite integration coverage for Driver's wiring of the shared
py-pitwall mechanics: Alembic migrations create the blueprint schema, the outbox relay
drains a real SQLite file onto a real RabbitMQ exchange, and the idempotent inbox dedupes
across a real transaction. Needs Docker (testcontainers) — mirrors
tests/conformance/python's use of testcontainers.community.rabbitmq.RabbitMqContainer.
No domain logic to exercise yet (Story 3.2+); this proves the skeleton's reliability
spine end to end, which the cross-language conformance harness's heartbeat-only scenario
does not (it never touches SQLite/outbox/inbox).
"""

from __future__ import annotations

import json
import threading
import uuid
from dataclasses import dataclass
from pathlib import Path

import pika
import pytest
from alembic import command
from alembic.config import Config as AlembicConfig
from driver.consumer import Handler
from pitwall.dlq import Policy
from pitwall.messaging import Binding, Bus, ConsumerOptions
from pitwall.persistence import (
    inbox_has_seen,
    inbox_mark_seen,
    open_db,
    outbox_enqueue,
    outbox_fetch_pending,
    outbox_mark_quarantined,
    outbox_mark_sent,
    outbox_record_failure,
    within_tx,
)
from pitwall.relay import Relay
from pitwall.validate import Validator
from testcontainers.community.rabbitmq import RabbitMqContainer

_SERVICE_DIR = Path(__file__).resolve().parent.parent


class _FakeLog:
    def info(self, *a, **k):
        pass

    def debug(self, *a, **k):
        pass

    def warning(self, *a, **k):
        pass

    def error(self, *a, **k):
        pass


def _migrated_db(tmp_path):
    db_path = str(tmp_path / "driver.db")
    cfg = AlembicConfig(str(_SERVICE_DIR / "alembic.ini"))
    cfg.set_main_option("script_location", str(_SERVICE_DIR / "migrations"))
    cfg.set_main_option("sqlalchemy.url", f"sqlite:///{db_path}")
    command.upgrade(cfg, "head")
    return db_path


@pytest.fixture(scope="module")
def rabbitmq_params():
    with RabbitMqContainer("rabbitmq:4.3-management-alpine") as rabbitmq:
        yield rabbitmq.get_connection_params()


def _amqp_url(params: pika.ConnectionParameters) -> str:
    creds = params.credentials
    return f"amqp://{creds.username}:{creds.password}@{params.host}:{params.port}{params.virtual_host}"


def test_outbox_row_drains_onto_a_real_exchange_and_is_marked_sent(tmp_path, rabbitmq_params):
    db_path = _migrated_db(tmp_path)
    conn = open_db(db_path)
    suffix = uuid.uuid4().hex[:8]
    exchange = f"test.driver.events.{suffix}"
    queue = f"test.driver.observe.{suffix}"

    row_id = str(uuid.uuid4())
    payload = b'{"type":"control.heartbeat","data":{}}'
    with within_tx(conn):
        outbox_enqueue(conn, row_id, "control.heartbeat", payload, created_at="2026-08-02T00:00:00.000Z")

    bus = Bus(_amqp_url(rabbitmq_params), exchange)
    bus.connect()
    bus._channel.queue_declare(queue, durable=True)
    bus._channel.queue_bind(queue, exchange, "control.heartbeat")

    relay = Relay(
        fetch_pending=lambda limit: outbox_fetch_pending(conn, limit),
        mark_sent=lambda rid, sent_at: outbox_mark_sent(conn, rid, sent_at),
        mark_quarantined=lambda rid, err: outbox_mark_quarantined(conn, rid, err),
        record_failure=lambda rid, err: outbox_record_failure(conn, rid, err),
        validate=lambda body: None,  # no /contract dir wired for this narrow drain test
        publish=bus.publish,
        interval_s=0.05,
        log=_FakeLog(),
    )

    sent = relay.drain_once()
    assert sent == 1

    rows_still_pending = outbox_fetch_pending(conn, 10)
    assert rows_still_pending == []

    deliveries = bus.consume(queue)
    d = next(deliveries)
    assert d.body == payload
    d.ack()

    bus.close()
    conn.close()


def test_relay_thread_drains_a_row_enqueued_after_run_starts(tmp_path, rabbitmq_params):
    db_path = _migrated_db(tmp_path)
    conn = open_db(db_path)
    suffix = uuid.uuid4().hex[:8]
    exchange = f"test.driver.events.{suffix}"
    queue = f"test.driver.observe.{suffix}"

    # The relay publishes on its OWN bus, in its own thread. Observing must happen on a
    # SEPARATE Bus/connection — pika's BlockingConnection/BlockingChannel is not safe
    # for concurrent use from more than one thread (consume() and publish() racing on
    # the same channel from different threads can hang the connection's I/O loop
    # entirely, not just corrupt a frame). This is the same reason
    # test_messaging_integration.py's parking-queue assertion uses a separate observer
    # Bus rather than reusing the producer's.
    publish_bus = Bus(_amqp_url(rabbitmq_params), exchange)
    publish_bus.connect()

    observer_bus = Bus(_amqp_url(rabbitmq_params), exchange)
    observer_bus.connect()
    observer_bus._channel.queue_declare(queue, durable=True)
    observer_bus._channel.queue_bind(queue, exchange, "control.heartbeat")

    # The relay's persistence callables must be bound to a connection opened ON the
    # relay's own thread — sqlite3's default check_same_thread=True makes a connection
    # unusable from any thread other than the one that created it. `conn` (opened above,
    # on the TEST's thread) is used for the enqueue below; the relay gets its own,
    # separate connection to the SAME file (mirrors driver.main's run_relay wiring,
    # fixed for exactly this reason after the original version of this test hung
    # forever — the relay's DB calls were silently raising sqlite3.ProgrammingError,
    # caught by the relay's own broad error handling, so nothing ever drained). kick()'s
    # cross-thread wake-up is already covered by test_relay.py's unit test; this test
    # uses a short poll interval instead, so it needs no reference to the Relay object
    # from outside the thread that owns its connection.
    stop = threading.Event()

    def run_relay():
        relay_conn = open_db(db_path)
        try:
            relay = Relay(
                fetch_pending=lambda limit: outbox_fetch_pending(relay_conn, limit),
                mark_sent=lambda rid, sent_at: outbox_mark_sent(relay_conn, rid, sent_at),
                mark_quarantined=lambda rid, err: outbox_mark_quarantined(relay_conn, rid, err),
                record_failure=lambda rid, err: outbox_record_failure(relay_conn, rid, err),
                validate=lambda body: None,
                publish=publish_bus.publish,
                interval_s=0.05,
                log=_FakeLog(),
            )
            relay.run(stop)
        finally:
            relay_conn.close()

    thread = threading.Thread(target=run_relay, name="relay", daemon=True)
    thread.start()

    row_id = str(uuid.uuid4())
    payload = b'{"type":"control.heartbeat","data":{}}'
    with within_tx(conn):
        outbox_enqueue(conn, row_id, "control.heartbeat", payload, created_at="2026-08-02T00:00:00.000Z")

    deliveries = observer_bus.consume(queue)
    d = next(deliveries)
    assert d.body == payload
    d.ack()

    stop.set()
    thread.join(timeout=5.0)
    assert not thread.is_alive()

    publish_bus.close()
    observer_bus.close()
    conn.close()


def test_inbox_dedupes_a_redelivered_envelope_id(tmp_path):
    db_path = _migrated_db(tmp_path)
    conn = open_db(db_path)

    envelope_id = str(uuid.uuid4())
    assert inbox_has_seen(conn, envelope_id) is False

    with within_tx(conn):
        assert inbox_has_seen(conn, envelope_id) is False
        inbox_mark_seen(conn, envelope_id, "driver.something", processed_at="2026-08-02T00:00:00.000Z")

    assert inbox_has_seen(conn, envelope_id) is True

    # A redelivery of the SAME envelope id must be recognized as a duplicate — the whole
    # point of the idempotent inbox (M6/NFR24).
    with within_tx(conn):
        assert inbox_has_seen(conn, envelope_id) is True

    conn.close()


# --- Consumer (Story 3.2): the minimal-profile safety net, end to end -----------------


def _repo_root() -> Path:
    d = Path(__file__).resolve()
    while d != d.parent:
        if (d / "contract" / "schemas" / "envelope.schema.json").is_file():
            return d
        d = d.parent
    raise RuntimeError("could not locate repo root")


@pytest.fixture(scope="module")
def validator() -> Validator:
    return Validator(str(_repo_root() / "contract"))


class _RecordingLog:
    def __init__(self):
        self.lines = []

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


def _lap_recorded_body(envelope_id: str, master_id: str, lap_number: int = 1) -> bytes:
    return json.dumps(
        {
            "id": envelope_id,
            "type": "lap.recorded",
            "source": "timing",
            "schemaVersion": 1,
            "envelopeVersion": 1,
            "occurredAt": "2026-08-06T12:00:00.000Z",
            "correlationId": envelope_id,
            "causationId": None,
            "data": {
                "masterId": master_id,
                "sessionId": "sess-1",
                "lapNumber": lap_number,
                "lapTimeMs": 42000,
                "at": "2026-08-06T12:00:00.000Z",
                "transponderId": None,
            },
        }
    ).encode("utf-8")


def _identity_resolved_body(envelope_id: str, master_id: str) -> bytes:
    return json.dumps(
        {
            "id": envelope_id,
            "type": "identity.resolved",
            "source": "identity",
            "schemaVersion": 1,
            "envelopeVersion": 1,
            "occurredAt": "2026-08-06T12:00:00.000Z",
            "correlationId": envelope_id,
            "causationId": envelope_id,
            "data": {"requestId": envelope_id, "email": "test@example.com", "masterId": master_id},
        }
    ).encode("utf-8")


@dataclass
class _ConsumerEnv:
    conn: object
    consumer_bus: Bus
    publish_bus: Bus
    observer_bus: Bus
    handler: Handler
    log: _RecordingLog
    timing_exchange: str
    identity_exchange: str
    queue: str
    observe_queue: str
    observe_history_queue: str = ""

    def deliveries(self):
        return self.consumer_bus.consume(self.queue)

    def drain_relay_once(self, validator: Validator) -> int:
        relay = Relay(
            fetch_pending=lambda limit: outbox_fetch_pending(self.conn, limit),
            mark_sent=lambda rid, sent_at: outbox_mark_sent(self.conn, rid, sent_at),
            mark_quarantined=lambda rid, err: outbox_mark_quarantined(self.conn, rid, err),
            record_failure=lambda rid, err: outbox_record_failure(self.conn, rid, err),
            validate=validator.validate_envelope_bytes,
            publish=self.publish_bus.publish,
            interval_s=0.05,
            log=self.log,
        )
        return relay.drain_once()

    def observe_profile_updated(self):
        d = next(self.observer_bus.consume(self.observe_queue))
        d.ack()
        return json.loads(d.body)

    def observe_history_appended(self, count: int) -> list:
        out = []
        for d in self.observer_bus.consume(self.observe_history_queue):
            d.ack()
            out.append(json.loads(d.body))
            if len(out) >= count:
                break
        return out

    def close(self):
        self.consumer_bus.close()
        self.publish_bus.close()
        self.observer_bus.close()
        self.conn.close()


@pytest.fixture()
def consumer_env(tmp_path, rabbitmq_params, validator: Validator) -> _ConsumerEnv:
    db_path = _migrated_db(tmp_path)
    conn = open_db(db_path)
    suffix = uuid.uuid4().hex[:8]
    driver_exchange = f"test.driver.events.{suffix}"
    timing_exchange = f"test.timing.events.{suffix}"
    identity_exchange = f"test.identity.events.{suffix}"
    queue = f"test.driver.profile-safety-net.{suffix}"
    dlx = f"test.driver.dlx.{suffix}"
    observe_queue = f"test.driver.observe.{suffix}"

    # Three SEPARATE connections (consume/publish/observe), same reason every other
    # test in this file uses one -- pika's BlockingChannel is not safe for concurrent
    # cross-thread use, and even same-thread reuse across a publish-then-consume
    # sequence on the SAME bus object used elsewhere in this file only works because
    # it stays strictly sequential; here consumer_bus both publishes (the test
    # fixtures onto timing/identity exchanges) and consumes (the queue), which stays
    # sequential too, but the observer needs its own connection since it consumes
    # concurrently with nothing else touching consumer_bus's channel by then.
    consumer_bus = Bus(_amqp_url(rabbitmq_params), driver_exchange)
    consumer_bus.connect()
    consumer_bus.declare_dlq_topology(
        ConsumerOptions(
            bindings=[
                Binding(source_exchange=timing_exchange, routing_keys=["lap.recorded", "session.ended"]),
                Binding(source_exchange=identity_exchange, routing_keys=["identity.resolved"]),
            ],
            queue_name=queue,
            prefetch=16,
            dlx_exchange=dlx,
        )
    )

    publish_bus = Bus(_amqp_url(rabbitmq_params), driver_exchange)
    publish_bus.connect()

    observe_history_queue = f"test.driver.observe-history.{suffix}"
    observer_bus = Bus(_amqp_url(rabbitmq_params), driver_exchange)
    observer_bus.connect()
    observer_bus._channel.queue_declare(observe_queue, durable=True)
    observer_bus._channel.queue_bind(observe_queue, driver_exchange, "driver.profile_updated")
    observer_bus._channel.queue_declare(observe_history_queue, durable=True)
    observer_bus._channel.queue_bind(observe_history_queue, driver_exchange, "driver.history_appended")

    log = _RecordingLog()
    handler = Handler(
        validate=validator.validate_envelope_bytes,
        conn=conn,
        source="driver",
        policy=Policy(max_attempts=5, base_ms=1000, multiplier=2, max_ms=60000),
        log=log,
        retry=consumer_bus.retry_to_dlx,
        park=consumer_bus.park_to_dlx,
    )

    env = _ConsumerEnv(
        conn=conn, consumer_bus=consumer_bus, publish_bus=publish_bus, observer_bus=observer_bus,
        handler=handler, log=log, timing_exchange=timing_exchange, identity_exchange=identity_exchange,
        queue=queue, observe_queue=observe_queue, observe_history_queue=observe_history_queue,
    )
    yield env
    env.close()


def _session_ended_body(envelope_id: str, rows: list[dict], session_id: str = "sess-1") -> bytes:
    return json.dumps(
        {
            "id": envelope_id,
            "type": "session.ended",
            "source": "timing",
            "schemaVersion": 1,
            "envelopeVersion": 1,
            "occurredAt": "2026-08-06T12:05:00.000Z",
            "correlationId": envelope_id,
            "causationId": None,
            "data": {"sessionId": session_id, "endedAt": "2026-08-06T12:05:00.000Z", "summary": rows},
        }
    ).encode("utf-8")


def test_lap_recorded_for_fresh_master_id_creates_profile_and_publishes_profile_updated(
    consumer_env: _ConsumerEnv, validator: Validator
):
    master_id = str(uuid.uuid4())
    envelope_id = str(uuid.uuid4())
    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="lap.recorded",
        body=_lap_recorded_body(envelope_id, master_id),
        properties=pika.BasicProperties(delivery_mode=2),
    )

    consumer_env.handler.process(next(consumer_env.deliveries()))

    row = consumer_env.conn.execute(
        "SELECT master_id, racing_number, kart_class, nickname FROM driver_profiles WHERE master_id = ?",
        (master_id,),
    ).fetchone()
    assert tuple(row) == (master_id, None, None, None)

    sent = consumer_env.drain_relay_once(validator)
    assert sent == 1

    observed = consumer_env.observe_profile_updated()
    assert observed["type"] == "driver.profile_updated"
    assert observed["data"]["masterId"] == master_id
    assert observed["data"] == {"masterId": master_id, "racingNumber": None, "kartClass": None, "nickname": None}


def test_redelivery_of_the_same_envelope_id_publishes_no_second_profile_updated(
    consumer_env: _ConsumerEnv, validator: Validator
):
    master_id = str(uuid.uuid4())
    envelope_id = str(uuid.uuid4())
    body = _lap_recorded_body(envelope_id, master_id)

    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="lap.recorded", body=body,
        properties=pika.BasicProperties(delivery_mode=2),
    )
    deliveries = consumer_env.deliveries()
    consumer_env.handler.process(next(deliveries))
    assert consumer_env.drain_relay_once(validator) == 1  # first (expected) publish drained + marked sent
    sent_after_first = consumer_env.conn.execute("SELECT COUNT(*) FROM outbox WHERE status='sent'").fetchone()[0]
    assert sent_after_first == 1

    # Redeliver the IDENTICAL envelope (same id) on the SAME queue.
    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="lap.recorded", body=body,
        properties=pika.BasicProperties(delivery_mode=2),
    )
    consumer_env.handler.process(next(deliveries))

    assert outbox_fetch_pending(consumer_env.conn, 10) == []  # no second row enqueued
    sent_after_redelivery = consumer_env.conn.execute("SELECT COUNT(*) FROM outbox WHERE status='sent'").fetchone()[0]
    assert sent_after_redelivery == 1  # still just the one -- no second publish
    assert consumer_env.log.has("debug", "duplicate envelope ignored")


def test_identity_resolved_for_fresh_master_id_also_creates_profile(consumer_env: _ConsumerEnv, validator: Validator):
    """Both trigger types share the safety net (AC2)."""
    master_id = str(uuid.uuid4())
    envelope_id = str(uuid.uuid4())
    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.identity_exchange, routing_key="identity.resolved",
        body=_identity_resolved_body(envelope_id, master_id),
        properties=pika.BasicProperties(delivery_mode=2),
    )

    consumer_env.handler.process(next(consumer_env.deliveries()))

    row = consumer_env.conn.execute(
        "SELECT master_id FROM driver_profiles WHERE master_id = ?", (master_id,)
    ).fetchone()
    assert row is not None
    assert consumer_env.drain_relay_once(validator) == 1


def test_second_lap_for_an_already_local_master_id_publishes_no_second_profile_updated(
    consumer_env: _ConsumerEnv, validator: Validator
):
    """Story-3.2 precedence (AC3), carried into Story 3.3: a SECOND, distinct lap for a
    masterId that already has a local profile must not touch the profile row and must
    not publish a second driver.profile_updated. In Story 3.3 the second lap is NO
    LONGER a pure no-op -- the lap itself IS appended -- so the log line is now
    "lap appended to history", not "profile already local, skipped"."""
    master_id = str(uuid.uuid4())
    deliveries = consumer_env.deliveries()

    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="lap.recorded",
        body=_lap_recorded_body(str(uuid.uuid4()), master_id, lap_number=1),
        properties=pika.BasicProperties(delivery_mode=2),
    )
    consumer_env.handler.process(next(deliveries))
    assert consumer_env.drain_relay_once(validator) == 1  # first (expected) publish drained + marked sent

    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="lap.recorded",
        body=_lap_recorded_body(str(uuid.uuid4()), master_id, lap_number=2),
        properties=pika.BasicProperties(delivery_mode=2),
    )
    consumer_env.handler.process(next(deliveries))

    row = consumer_env.conn.execute(
        "SELECT master_id, racing_number, kart_class, nickname FROM driver_profiles WHERE master_id = ?",
        (master_id,),
    ).fetchone()
    assert tuple(row) == (master_id, None, None, None)  # profile untouched
    assert outbox_fetch_pending(consumer_env.conn, 10) == []  # no second row enqueued
    sent_count = consumer_env.conn.execute("SELECT COUNT(*) FROM outbox WHERE status='sent'").fetchone()[0]
    assert sent_count == 1  # still just the one profile_updated -- no second publish
    # Both laps ARE stored (the second lap is not dropped).
    laps = consumer_env.conn.execute(
        "SELECT lap_number FROM driver_laps WHERE master_id = ? AND session_id = ? ORDER BY lap_number",
        (master_id, "sess-1"),
    ).fetchall()
    assert [r[0] for r in laps] == [1, 2]
    assert consumer_env.log.has("info", "lap appended to history")


# --- Consumer (Story 3.3): lap history + session summaries, end to end ----------------


def test_lap_recorded_appends_a_lap_row_end_to_end(consumer_env: _ConsumerEnv, validator: Validator):
    """AC1: a real lap.recorded through the real broker appends a driver_laps row (and,
    via the safety net, a driver_profiles row -- AC3)."""
    master_id = str(uuid.uuid4())
    envelope_id = str(uuid.uuid4())
    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="lap.recorded",
        body=_lap_recorded_body(envelope_id, master_id, lap_number=7),
        properties=pika.BasicProperties(delivery_mode=2),
    )

    consumer_env.handler.process(next(consumer_env.deliveries()))

    assert consumer_env.conn.execute(
        "SELECT master_id FROM driver_profiles WHERE master_id = ?", (master_id,)
    ).fetchone() is not None
    lap = consumer_env.conn.execute(
        "SELECT lap_number, lap_time_ms, source_event_id FROM driver_laps WHERE master_id = ? AND session_id = ?",
        (master_id, "sess-1"),
    ).fetchone()
    assert tuple(lap) == (7, 42000, envelope_id)


def test_redelivered_lap_recorded_appends_no_second_lap_row(consumer_env: _ConsumerEnv, validator: Validator):
    """AC1 idempotency: the SAME lap.recorded redelivered stores no second lap row."""
    master_id = str(uuid.uuid4())
    envelope_id = str(uuid.uuid4())
    body = _lap_recorded_body(envelope_id, master_id, lap_number=7)

    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="lap.recorded", body=body,
        properties=pika.BasicProperties(delivery_mode=2),
    )
    deliveries = consumer_env.deliveries()
    consumer_env.handler.process(next(deliveries))

    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="lap.recorded", body=body,
        properties=pika.BasicProperties(delivery_mode=2),
    )
    consumer_env.handler.process(next(deliveries))

    count = consumer_env.conn.execute(
        "SELECT COUNT(*) FROM driver_laps WHERE master_id = ? AND session_id = ?", (master_id, "sess-1")
    ).fetchone()[0]
    assert count == 1


def test_session_ended_stores_summaries_and_publishes_history_per_driver_end_to_end(
    consumer_env: _ConsumerEnv, validator: Validator
):
    """AC2/AC3: a real session.ended with a 2-row summary (one fresh masterId, one that
    already has a profile) stores 2 summaries and publishes 2 driver.history_appended;
    the fresh masterId also gains a driver_profiles row."""
    existing_id = str(uuid.uuid4())
    fresh_id = str(uuid.uuid4())

    # Give `existing_id` a profile first via a lap.
    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="lap.recorded",
        body=_lap_recorded_body(str(uuid.uuid4()), existing_id, lap_number=1),
        properties=pika.BasicProperties(delivery_mode=2),
    )
    deliveries = consumer_env.deliveries()
    consumer_env.handler.process(next(deliveries))
    consumer_env.drain_relay_once(validator)  # drain the profile_updated so it doesn't clog

    rows = [
        {"masterId": existing_id, "bestLapMs": 41980, "lapCount": 12},
        {"masterId": fresh_id, "bestLapMs": 43110, "lapCount": 9},
    ]
    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="session.ended",
        body=_session_ended_body(str(uuid.uuid4()), rows),
        properties=pika.BasicProperties(delivery_mode=2),
    )
    consumer_env.handler.process(next(deliveries))

    # Two summaries stored.
    stored = {
        r[0]: (r[1], r[2])
        for r in consumer_env.conn.execute(
            "SELECT master_id, best_lap_ms, lap_count FROM driver_session_summaries WHERE session_id = ?",
            ("sess-1",),
        ).fetchall()
    }
    assert stored == {existing_id: (41980, 12), fresh_id: (43110, 9)}
    # The fresh masterId gained a profile (AC3).
    assert consumer_env.conn.execute(
        "SELECT master_id FROM driver_profiles WHERE master_id = ?", (fresh_id,)
    ).fetchone() is not None

    # Drain the outbox and observe exactly 2 driver.history_appended on the bus.
    consumer_env.drain_relay_once(validator)
    observed = consumer_env.observe_history_appended(2)
    by_master = {m["data"]["masterId"]: m["data"] for m in observed}
    assert by_master[existing_id] == {"masterId": existing_id, "sessionId": "sess-1", "bestLapMs": 41980, "lapCount": 12}
    assert by_master[fresh_id] == {"masterId": fresh_id, "sessionId": "sess-1", "bestLapMs": 43110, "lapCount": 9}


def test_redelivered_session_ended_publishes_no_second_history(consumer_env: _ConsumerEnv, validator: Validator):
    """AC2 idempotency: the SAME session.ended redelivered publishes no second
    history_appended and stores no duplicate summary rows."""
    master_id = str(uuid.uuid4())
    body = _session_ended_body(str(uuid.uuid4()), [{"masterId": master_id, "bestLapMs": 41980, "lapCount": 12}])

    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="session.ended", body=body,
        properties=pika.BasicProperties(delivery_mode=2),
    )
    deliveries = consumer_env.deliveries()
    consumer_env.handler.process(next(deliveries))
    assert consumer_env.drain_relay_once(validator) == 2  # profile_updated + history_appended
    sent_after_first = consumer_env.conn.execute("SELECT COUNT(*) FROM outbox WHERE status='sent'").fetchone()[0]

    consumer_env.consumer_bus._channel.basic_publish(
        exchange=consumer_env.timing_exchange, routing_key="session.ended", body=body,
        properties=pika.BasicProperties(delivery_mode=2),
    )
    consumer_env.handler.process(next(deliveries))

    assert outbox_fetch_pending(consumer_env.conn, 10) == []  # nothing new enqueued
    sent_after_redelivery = consumer_env.conn.execute("SELECT COUNT(*) FROM outbox WHERE status='sent'").fetchone()[0]
    assert sent_after_redelivery == sent_after_first  # no second publish
    assert consumer_env.conn.execute(
        "SELECT COUNT(*) FROM driver_session_summaries WHERE master_id = ?", (master_id,)
    ).fetchone()[0] == 1
