"""Real-RabbitMQ integration tests for pitwall.messaging (mirrors the Go library's
DLQ/reconnect integration coverage). Needs Docker: a broker at PITWALL_TEST_AMQP_URL
(default amqp://guest:guest@localhost:5672/). Skipped automatically if unreachable —
mirrors the repo's existing pattern of gating on a real broker rather than mocking the
one behavior this module exists to get right.
"""

import os
import uuid

import pika
import pytest

from pitwall.messaging import Binding, Bus, ConsumerOptions

AMQP_URL = os.environ.get("PITWALL_TEST_AMQP_URL", "amqp://guest:guest@localhost:5672/")


def _broker_available() -> bool:
    try:
        conn = pika.BlockingConnection(pika.URLParameters(AMQP_URL))
        conn.close()
        return True
    except Exception:
        return False


pytestmark = pytest.mark.skipif(not _broker_available(), reason="no RabbitMQ reachable at PITWALL_TEST_AMQP_URL")


@pytest.fixture()
def unique_names():
    suffix = uuid.uuid4().hex[:8]
    return {
        "own_exchange": f"test.driver.events.{suffix}",
        "source_exchange": f"test.timing.events.{suffix}",
        "second_source_exchange": f"test.identity.events.{suffix}",
        "queue": f"test.driver.lap-recorded.{suffix}",
        "dlx": f"test.driver.dlx.{suffix}",
    }


def test_publish_and_consume_own_exchange_round_trip(unique_names):
    bus = Bus(AMQP_URL, unique_names["own_exchange"])
    bus.connect()
    try:
        bus._channel.queue_declare(unique_names["queue"], durable=True)
        bus._channel.queue_bind(unique_names["queue"], unique_names["own_exchange"], "control.heartbeat")

        bus.publish("control.heartbeat", b'{"service":"driver"}')

        deliveries = bus.consume(unique_names["queue"])
        d = next(deliveries)
        assert d.body == b'{"service":"driver"}'
        assert d.retry_count == 0
        d.ack()
    finally:
        bus.close()


def test_dlq_topology_retry_then_park(unique_names):
    bus = Bus(AMQP_URL, unique_names["own_exchange"])
    bus.connect()
    try:
        opts = ConsumerOptions(
            bindings=[Binding(source_exchange=unique_names["source_exchange"], routing_keys=["lap.recorded"])],
            queue_name=unique_names["queue"],
            prefetch=16,
            dlx_exchange=unique_names["dlx"],
        )
        bus.declare_dlq_topology(opts)

        # Publish directly onto the source exchange (impersonating the producer).
        bus._channel.exchange_declare(exchange=unique_names["source_exchange"], exchange_type="topic", durable=True)
        bus._channel.basic_publish(
            exchange=unique_names["source_exchange"],
            routing_key="lap.recorded",
            body=b"poison",
            properties=pika.BasicProperties(delivery_mode=2),
        )

        deliveries = bus.consume(unique_names["queue"])
        d = next(deliveries)
        assert d.body == b"poison"
        assert d.retry_count == 0

        # Simulate a processing failure: retry with a short TTL so the test doesn't
        # wait long for the dead-letter round-trip back to the work queue.
        bus.retry_to_dlx(d.body, delay_ms=50, next_retries=1)
        d.ack()  # ack the ORIGINAL only after the retry republish succeeds (NFR6)

        # After the TTL, the message dead-letters back onto the work queue with the
        # incremented retry-count header.
        d2 = next(deliveries)
        assert d2.body == b"poison"
        assert d2.retry_count == 1

        # Now park it terminally.
        bus.park_to_dlx(d2.body, reason="max attempts exceeded")
        d2.ack()
    finally:
        bus.close()

    # The parking queue holds it — verify via a SEPARATE Bus/channel (pika's
    # BlockingChannel.consume() generator is stateful per channel: you cannot start a
    # second consumer generator on a different queue over the same channel without
    # cancelling the first — realistic anyway, since in production the parking queue
    # is drained by an entirely different consumer, e.g. Control Room, Epic 12).
    observer = Bus(AMQP_URL, unique_names["own_exchange"])
    observer.connect()
    try:
        parking_deliveries = observer.consume(f"{unique_names['queue']}.parking")
        d3 = next(parking_deliveries)
        assert d3.body == b"poison"
        d3.ack()
    finally:
        observer.close()


def test_one_queue_receives_from_two_producer_exchanges(unique_names):
    """Story 3.2's actual need: Driver's profile safety net consumes lap.recorded off
    timing.events AND identity.resolved off identity.events into ONE durable queue
    sharing ONE DLQ topology. Proves both bindings work against a real broker and that
    a message published on EITHER exchange lands on the same queue."""
    bus = Bus(AMQP_URL, unique_names["own_exchange"])
    bus.connect()
    try:
        opts = ConsumerOptions(
            bindings=[
                Binding(source_exchange=unique_names["source_exchange"], routing_keys=["lap.recorded"]),
                Binding(
                    source_exchange=unique_names["second_source_exchange"], routing_keys=["identity.resolved"]
                ),
            ],
            queue_name=unique_names["queue"],
            prefetch=16,
            dlx_exchange=unique_names["dlx"],
        )
        bus.declare_dlq_topology(opts)

        bus._channel.basic_publish(
            exchange=unique_names["source_exchange"],
            routing_key="lap.recorded",
            body=b"from-timing",
            properties=pika.BasicProperties(delivery_mode=2),
        )
        bus._channel.basic_publish(
            exchange=unique_names["second_source_exchange"],
            routing_key="identity.resolved",
            body=b"from-identity",
            properties=pika.BasicProperties(delivery_mode=2),
        )

        deliveries = bus.consume(unique_names["queue"])
        bodies = set()
        for _ in range(2):
            d = next(deliveries)
            bodies.add(d.body)
            d.ack()
        assert bodies == {b"from-timing", b"from-identity"}
    finally:
        bus.close()
