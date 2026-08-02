"""Broker-free unit tests for pitwall.messaging's pure/mockable pieces (mirrors
libs/go-pitwall/messaging/dlq_test.go's naming-helper coverage). Real-broker DLQ
topology + consume/retry/park behavior is covered in test_messaging_integration.py
(needs Docker)."""

from unittest.mock import MagicMock

import pytest

from pitwall.messaging import (
    PARK_REASON_HEADER,
    PARK_ROUTING_KEY,
    REDELIVER_ROUTING_KEY,
    RETRY_COUNT_HEADER,
    RETRY_ROUTING_KEY,
    Bus,
    ConsumerOptions,
    _parse_retry_count,
    parking_queue_name,
    retry_queue_name,
    run_with_reconnect,
)
from pika.exceptions import AMQPConnectionError


def test_retry_and_parking_queue_names():
    assert retry_queue_name("driver.lap-recorded") == "driver.lap-recorded.retry"
    assert parking_queue_name("driver.lap-recorded") == "driver.lap-recorded.parking"


@pytest.mark.parametrize(
    "headers,want",
    [
        (None, 0),
        ({}, 0),
        ({RETRY_COUNT_HEADER: 3}, 3),
        ({RETRY_COUNT_HEADER: "not-an-int"}, 0),
    ],
)
def test_parse_retry_count_defensive(headers, want):
    assert _parse_retry_count(headers) == want


def test_declare_dlq_topology_requires_dlx_exchange():
    bus = Bus("amqp://localhost", "driver.events")
    bus._channel = MagicMock()
    opts = ConsumerOptions(
        source_exchange="frontend.events", queue_name="driver.lookup", routing_keys=["identity.resolved"],
        prefetch=16, dlx_exchange="",
    )
    with pytest.raises(ValueError, match="dlx_exchange"):
        bus.declare_dlq_topology(opts)


def test_declare_dlq_topology_declares_full_topology():
    bus = Bus("amqp://localhost", "driver.events")
    ch = MagicMock()
    bus._channel = ch
    opts = ConsumerOptions(
        source_exchange="timing.events",
        queue_name="driver.lap-recorded",
        routing_keys=["lap.recorded"],
        prefetch=16,
        dlx_exchange="driver.dlx",
    )
    bus.declare_dlq_topology(opts)

    ch.exchange_declare.assert_any_call(exchange="timing.events", exchange_type="topic", durable=True)
    ch.exchange_declare.assert_any_call(exchange="driver.dlx", exchange_type="direct", durable=True)
    ch.queue_declare.assert_any_call(
        "driver.lap-recorded.retry",
        durable=True,
        arguments={"x-dead-letter-exchange": "driver.dlx", "x-dead-letter-routing-key": REDELIVER_ROUTING_KEY},
    )
    ch.queue_declare.assert_any_call("driver.lap-recorded.parking", durable=True)
    ch.queue_declare.assert_any_call(
        "driver.lap-recorded",
        durable=True,
        arguments={"x-dead-letter-exchange": "driver.dlx", "x-dead-letter-routing-key": PARK_ROUTING_KEY},
    )
    ch.queue_bind.assert_any_call("driver.lap-recorded.retry", "driver.dlx", RETRY_ROUTING_KEY)
    ch.queue_bind.assert_any_call("driver.lap-recorded", "driver.dlx", REDELIVER_ROUTING_KEY)
    ch.queue_bind.assert_any_call("driver.lap-recorded.parking", "driver.dlx", PARK_ROUTING_KEY)
    ch.queue_bind.assert_any_call("driver.lap-recorded", "timing.events", "lap.recorded")
    ch.basic_qos.assert_called_once_with(prefetch_count=16)
    assert bus._dlx == "driver.dlx"


def test_retry_to_dlx_carries_backoff_ttl_and_headers():
    bus = Bus("amqp://localhost", "driver.events")
    ch = MagicMock()
    bus._channel = ch
    bus._dlx = "driver.dlx"

    bus.retry_to_dlx(b"payload", delay_ms=2000, next_retries=2)

    _, kwargs = ch.basic_publish.call_args
    assert kwargs["exchange"] == "driver.dlx"
    assert kwargs["routing_key"] == RETRY_ROUTING_KEY
    assert kwargs["properties"].expiration == "2000"
    assert kwargs["properties"].headers[RETRY_COUNT_HEADER] == 2


def test_park_to_dlx_carries_reason_and_no_ttl():
    bus = Bus("amqp://localhost", "driver.events")
    ch = MagicMock()
    bus._channel = ch
    bus._dlx = "driver.dlx"

    bus.park_to_dlx(b"payload", reason="malformed")

    _, kwargs = ch.basic_publish.call_args
    assert kwargs["routing_key"] == PARK_ROUTING_KEY
    assert kwargs["properties"].expiration is None
    assert kwargs["properties"].headers[PARK_REASON_HEADER] == "malformed"


def test_run_with_reconnect_stops_on_clean_return():
    calls = []

    def connect_and_run():
        calls.append(1)

    run_with_reconnect(connect_and_run, stop=lambda: len(calls) >= 1, log=MagicMock())
    assert len(calls) == 1


def test_run_with_reconnect_backs_off_and_retries_on_connection_error(monkeypatch):
    slept = []
    monkeypatch.setattr("pitwall.messaging.time.sleep", lambda s: slept.append(s))

    attempts = []

    def connect_and_run():
        attempts.append(1)
        if len(attempts) < 3:
            raise AMQPConnectionError("connection refused")

    run_with_reconnect(connect_and_run, stop=lambda: len(attempts) >= 3, log=MagicMock(), base_delay_s=1.0, max_delay_s=30.0)

    assert len(attempts) == 3
    assert slept == [1.0, 2.0]  # exponential backoff between the two failures
