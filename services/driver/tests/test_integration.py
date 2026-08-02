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

import threading
import uuid

import pika
import pytest
from alembic import command
from alembic.config import Config as AlembicConfig
from pitwall.messaging import Bus
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
from testcontainers.community.rabbitmq import RabbitMqContainer

_SERVICE_DIR = __import__("pathlib").Path(__file__).resolve().parent.parent


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
