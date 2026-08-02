"""The pika-based (sync, blocking — Q&A Round 34/Q34.2) bus-side blueprint mechanics:
own-exchange declare, publish, a consumer queue bound to a producer's exchange, and the
DLQ/TTL-retry/parking topology. Mirrors libs/go-pitwall/messaging (consumer.go/dlq.go),
minus the Go library's channel-based reconnect supervisor — pika's BlockingConnection
is inherently synchronous, so reconnection here is a retry-the-whole-connect-and-consume
-loop wrapper (run_consumer_forever) rather than a background goroutine swapping a live
channel under a mutex. Carries mechanics ONLY: no service exchange/queue names or
domain payload shapes (passed in as ConsumerOptions).
"""

from __future__ import annotations

import threading
import time
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field
from typing import Any

import pika
from pika.exceptions import AMQPConnectionError, AMQPError, StreamLostError

# Consumer-side DLQ topology routing keys on the DLX (mirrors Go's dlq.go).
RETRY_ROUTING_KEY = "retry"
REDELIVER_ROUTING_KEY = "redeliver"
PARK_ROUTING_KEY = "park"

# Message headers.
RETRY_COUNT_HEADER = "x-pitwall-retry-count"
PARK_REASON_HEADER = "x-pitwall-park-reason"


def retry_queue_name(work_queue: str) -> str:
    return f"{work_queue}.retry"


def parking_queue_name(work_queue: str) -> str:
    return f"{work_queue}.parking"


@dataclass(frozen=True)
class ConsumerOptions:
    source_exchange: str  # the PRODUCER's exchange to bind to (e.g. "timing.events")
    queue_name: str  # this consumer's durable queue
    routing_keys: list[str]  # binding keys
    prefetch: int  # QoS: max in-flight unacked deliveries
    dlx_exchange: str  # the consumer-side dead-letter exchange (e.g. "driver.dlx")


@dataclass
class Delivery:
    """The broker-agnostic view of a consumed message (mirrors Go's Delivery
    interface): Ack() -> processed (or a dedupe no-op), drop it. Nack(requeue) ->
    processing failed; requeue=False discards/dead-letters it."""

    body: bytes
    retry_count: int
    _channel: Any = field(repr=False)
    _delivery_tag: int = field(repr=False)

    def ack(self) -> None:
        self._channel.basic_ack(delivery_tag=self._delivery_tag)

    def nack(self, requeue: bool = False) -> None:
        self._channel.basic_nack(delivery_tag=self._delivery_tag, requeue=requeue)


def _parse_retry_count(headers: dict | None) -> int:
    """Reads the retry-count header defensively: any missing or non-integer value is
    treated as 0 (a fresh, never-retried message)."""
    if not headers:
        return 0
    v = headers.get(RETRY_COUNT_HEADER)
    return v if isinstance(v, int) else 0


class Bus:
    """Owns the AMQP connection + channel for one connection generation. Declares the
    service's OWN durable topic exchange (for the heartbeat + outbox it publishes) and,
    separately, a durable queue bound to a PRODUCER's exchange (for the domain events it
    consumes) with full DLQ/TTL-retry/parking topology."""

    def __init__(self, url: str, own_exchange: str):
        self._url = url
        self._exchange = own_exchange
        self._connection: pika.BlockingConnection | None = None
        self._channel: Any = None
        self._dlx: str | None = None
        # pika's BlockingConnection/BlockingChannel is not safe for concurrent use
        # from multiple threads (a raw AMQP frame write could interleave and corrupt
        # the wire). publish() is the one method this skeleton calls from more than
        # one thread (the heartbeat emitter and the outbox relay both publish), so it
        # is serialized; consume()/declare_dlq_topology()/close() are single-thread
        # (main-thread-only) in this story and are not additionally guarded.
        self._publish_lock = threading.Lock()

    def connect(self) -> None:
        """Dials the broker, opens a channel, and declares the service's own durable
        exchange (fail-fast at startup)."""
        self._connection = pika.BlockingConnection(pika.URLParameters(self._url))
        self._channel = self._connection.channel()
        self._channel.exchange_declare(exchange=self._exchange, exchange_type="topic", durable=True)

    def is_connected(self) -> bool:
        """Reports whether the underlying broker connection is still open (used by a
        supervising reconnect loop to detect a dropped connection between publishes)."""
        return self._connection is not None and self._connection.is_open

    def publish(self, routing_key: str, body: bytes) -> None:
        """Sends a persistent JSON message to the service's OWN exchange (e.g. the 1 s
        heartbeat, or an outbox row). Safe to call from multiple threads."""
        if self._channel is None:
            raise RuntimeError("Bus.publish called before connect()")
        with self._publish_lock:
            self._channel.basic_publish(
                exchange=self._exchange,
                routing_key=routing_key,
                body=body,
                properties=pika.BasicProperties(content_type="application/json", delivery_mode=2),
            )

    def declare_dlq_topology(self, opts: ConsumerOptions) -> None:
        """Declares the full consumer-side DLQ topology for the given work queue and
        binds it to its source exchange: a retry queue (TTL-per-message dead-lettering
        back to the work queue), a terminal parking queue, and the work queue itself
        carrying dead-letter args pointing at the parking route (a safety net: an
        unmodelled reject parks rather than drops)."""
        if not opts.dlx_exchange:
            raise ValueError("declare_dlq_topology: ConsumerOptions.dlx_exchange must be set")
        ch = self._channel
        retry_q = retry_queue_name(opts.queue_name)
        parking_q = parking_queue_name(opts.queue_name)

        ch.exchange_declare(exchange=opts.source_exchange, exchange_type="topic", durable=True)
        ch.exchange_declare(exchange=opts.dlx_exchange, exchange_type="direct", durable=True)

        ch.queue_declare(
            retry_q,
            durable=True,
            arguments={
                "x-dead-letter-exchange": opts.dlx_exchange,
                "x-dead-letter-routing-key": REDELIVER_ROUTING_KEY,
            },
        )
        ch.queue_declare(parking_q, durable=True)
        ch.queue_declare(
            opts.queue_name,
            durable=True,
            arguments={
                "x-dead-letter-exchange": opts.dlx_exchange,
                "x-dead-letter-routing-key": PARK_ROUTING_KEY,
            },
        )

        ch.queue_bind(retry_q, opts.dlx_exchange, RETRY_ROUTING_KEY)
        ch.queue_bind(opts.queue_name, opts.dlx_exchange, REDELIVER_ROUTING_KEY)
        ch.queue_bind(parking_q, opts.dlx_exchange, PARK_ROUTING_KEY)
        for key in opts.routing_keys:
            ch.queue_bind(opts.queue_name, opts.source_exchange, key)

        ch.basic_qos(prefetch_count=opts.prefetch)
        self._dlx = opts.dlx_exchange

    def publish_to_dlx(
        self,
        routing_key: str,
        body: bytes,
        expiration_ms: int = 0,
        retry_count: int = 0,
        park_reason: str = "",
    ) -> None:
        """Publishes body to the consumer's DLX with the given routing key. For a
        retry, expiration_ms is the per-hop backoff TTL and retry_count the incremented
        hop count; for a park, expiration_ms is 0 and park_reason records why."""
        headers: dict[str, Any] = {RETRY_COUNT_HEADER: retry_count}
        if park_reason:
            headers[PARK_REASON_HEADER] = park_reason
        props_kwargs: dict[str, Any] = {
            "content_type": "application/json",
            "delivery_mode": 2,
            "headers": headers,
        }
        if expiration_ms > 0:
            props_kwargs["expiration"] = str(expiration_ms)
        self._channel.basic_publish(
            exchange=self._dlx,
            routing_key=routing_key,
            body=body,
            properties=pika.BasicProperties(**props_kwargs),
        )

    def retry_to_dlx(self, body: bytes, delay_ms: int, next_retries: int) -> None:
        """Republishes a failed message to the retry queue with the backoff TTL."""
        self.publish_to_dlx(RETRY_ROUTING_KEY, body, expiration_ms=delay_ms, retry_count=next_retries)

    def park_to_dlx(self, body: bytes, reason: str) -> None:
        """Routes a message terminally to the parking queue with a reason."""
        self.publish_to_dlx(PARK_ROUTING_KEY, body, park_reason=reason)

    def consume(self, queue_name: str) -> Iterator[Delivery]:
        """Yields deliveries from queue_name with manual-ack discipline (the caller
        acks only after the local state change durably commits, NFR6)."""
        for method, properties, body in self._channel.consume(queue_name, auto_ack=False):
            yield Delivery(
                body=body,
                retry_count=_parse_retry_count(properties.headers),
                _channel=self._channel,
                _delivery_tag=method.delivery_tag,
            )

    def close(self) -> None:
        if self._connection is not None and self._connection.is_open:
            self._connection.close()


def run_with_reconnect(
    connect_and_run: Callable[[], None],
    stop: Callable[[], bool],
    log: Any,
    base_delay_s: float = 1.0,
    max_delay_s: float = 30.0,
) -> None:
    """Supervises connect_and_run across broker restarts: calls it, and on a broker
    connection error backs off (capped exponential) and calls it again, until stop()
    returns True. connect_and_run is expected to (re)connect, (re)declare topology, and
    block consuming until the connection drops or stop() becomes true (mirrors the Go
    supervisor's re-dial/re-declare/re-subscribe loop, collapsed to one retried callable
    since pika's BlockingConnection has no separate "swap the live channel" seam).

    Catches pika's own AMQP-level exceptions AND plain OSError: a failed DNS lookup
    (socket.gaierror) or a refused/timed-out TCP connect (ConnectionRefusedError,
    TimeoutError) are both OSError subclasses that pika does NOT wrap into one of its
    own exception types -- they propagate raw from the underlying socket call. Found by
    actually running a built container against a broker hostname that was not yet
    resolvable at startup (a real, common race during Compose/orchestrator bring-up):
    the original AMQP-only tuple let that crash the whole service instead of retrying,
    not by inspection."""
    delay = base_delay_s
    while not stop():
        try:
            connect_and_run()
            delay = base_delay_s  # a clean return (stop requested) resets backoff
        except (AMQPConnectionError, StreamLostError, AMQPError, OSError) as e:
            log.error("bus connection lost, reconnecting", error=str(e), delaySeconds=delay)
            time.sleep(delay)
            delay = min(delay * 2, max_delay_s)
