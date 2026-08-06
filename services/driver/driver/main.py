"""Driver's entrypoint: runs its own Alembic migrations, connects to the bus (retried,
supervised across broker restarts), declares its own durable exchange, emits a 1 s
heartbeat, drains its own outbox via the relay, consumes the minimal-profile safety
net (Story 3.2 -- lap.recorded off timing.events + identity.resolved off
identity.events, one queue), logs structured JSON, and shuts down gracefully on
SIGTERM/SIGINT (mirrors services/timing/cmd/timing/main.go's Story-1.3 shape plus its
Story-1.4/1.10 outbox-relay + reconnect-supervisor shape, translated to Python's
threading model — see pitwall.messaging.run_with_reconnect's docstring for why this
is a whole-cycle retry rather than Go's live channel swap).
"""

from __future__ import annotations

import os
import signal
import threading

from alembic import command
from alembic.config import Config as AlembicConfig
from pika.exceptions import AMQPConnectionError
from pitwall.dlq import Policy
from pitwall.envelope import new_heartbeat_envelope
from pitwall.heartbeat import Emitter
from pitwall.logging import new_logger
from pitwall.messaging import Binding, Bus, ConsumerOptions, run_with_reconnect
from pitwall.persistence import (
    open_db,
    outbox_fetch_pending,
    outbox_mark_quarantined,
    outbox_mark_sent,
    outbox_record_failure,
)
from pitwall.relay import Relay
from pitwall.validate import Validator, resolve_contract_dir

from driver.config import ConfigError, load_config
from driver.consumer import IDENTITY_RESOLVED_TYPE, LAP_RECORDED_TYPE, Handler

DRIVER_EXCHANGE = "driver.events"
CONSUMER_QUEUE = "driver.profile-safety-net"
CONSUMER_DLX = "driver.dlx"

# How often connect_and_run polls the bus connection for a mid-run drop while otherwise
# blocked waiting for the shutdown signal. Coarse on purpose: this is a liveness check,
# not a latency-sensitive path -- a real drop is also usually observed sooner via a
# failed publish inside the heartbeat/relay threads' own logs.
_CONNECTION_POLL_S = 1.0


def _run_migrations(db_path: str, service_dir: str) -> None:
    # Set sqlalchemy.url directly on the Config object rather than mutating the
    # process-global os.environ["DB_PATH"] — migrations/env.py's own os.environ.get
    # fallback remains for the standalone `alembic upgrade head` CLI use case, but the
    # in-process path here must not leak a global side effect into the rest of the
    # service (or into tests running main() more than once in the same interpreter).
    cfg = AlembicConfig(os.path.join(service_dir, "alembic.ini"))
    cfg.set_main_option("script_location", os.path.join(service_dir, "migrations"))
    cfg.set_main_option("sqlalchemy.url", f"sqlite:///{db_path}")
    command.upgrade(cfg, "head")


def _default_service_dir() -> str:
    """Locates alembic.ini + migrations/. Deliberately NOT derived from __file__: once
    `driver` is pip-installed non-editable (the Docker build does a real install, not
    `-e`), the package lands in site-packages and __file__-based dirname tricks no
    longer point at the source tree where alembic.ini actually lives. Instead this
    defaults to the CURRENT WORKING DIRECTORY — the Docker image sets WORKDIR to the
    directory alembic.ini/migrations/ are COPYed into (see Dockerfile) — with an env
    override (SERVICE_DIR) for local dev runs from an arbitrary cwd."""
    return os.environ.get("SERVICE_DIR") or os.getcwd()


def _join_and_log(thread: threading.Thread, timeout_ms: int, log, name: str) -> None:
    """Bounds the wait for thread to finish and distinguishes a clean stop from a
    timeout — silently proceeding either way (as before) left no trace that the
    shutdown budget had actually been exceeded."""
    thread.join(timeout=timeout_ms / 1000.0)
    if thread.is_alive():
        log.error(f"{name} thread did not stop within shutdown_timeout_ms", shutdownTimeoutMs=timeout_ms)


def main() -> int:
    try:
        # os.environ.get defaults to None for a missing key (unlike Go's os.Getenv,
        # which returns ""); config.load_config's helpers assume a string always comes
        # back, matching how the test suite's fake getenv already behaves.
        cfg = load_config(lambda key: os.environ.get(key, ""))
    except ConfigError as e:
        # Config isn't loaded yet, so this is the one place a bare-ish message to
        # stderr is acceptable — there is no correlationId/service context to log
        # structured JSON with before config resolves. Everything after this point
        # uses the structured logger.
        import sys

        print(f"driver: configuration error: {e}", file=sys.stderr)
        return 1

    log = new_logger(service=cfg.service_name, correlation_id=cfg.instance_id, level=cfg.log_level)
    log.info("starting driver", instanceId=cfg.instance_id)

    _run_migrations(cfg.db_path, _default_service_dir())
    log.info("migrations applied", dbPath=cfg.db_path)

    contract_dir = resolve_contract_dir(cfg.contract_dir or None)
    validator = Validator(contract_dir)

    amqp_url = (
        f"amqp://{cfg.rabbitmq_user}:{cfg.rabbitmq_password}"
        f"@{cfg.rabbitmq_host}:{cfg.rabbitmq_port}{cfg.rabbitmq_vhost}"
    )

    stop = threading.Event()

    def handle_signal(signum, frame):
        log.info("shutdown signal received", signal=signum)
        stop.set()

    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)

    def build_heartbeat(now):
        return new_heartbeat_envelope(
            service=cfg.service_name, instance_id=cfg.instance_id, correlation_id=cfg.instance_id, now=now
        )

    def validate_heartbeat(env):
        validator.validate_heartbeat(env.model_dump(mode="json", by_alias=True, exclude_none=False))

    # Holds the CURRENT connection cycle's live Relay instance (if any) so the
    # consumer thread's notify callback can kick it after enqueueing a fresh
    # driver.profile_updated row -- the Relay itself is constructed inside
    # run_relay, on the relay thread, so it cannot simply be a closed-over local.
    relay_box: dict[str, Relay] = {}

    def notify_relay() -> None:
        relay = relay_box.get("relay")
        if relay is not None:
            relay.kick()

    def run_relay(publish, cycle_stop: threading.Event) -> None:
        """The relay thread's own entrypoint. Opens ITS OWN SQLite connection here, on
        this thread — sqlite3's default check_same_thread=True makes a connection
        unusable from any thread other than the one that opened it, so relay_conn must
        NOT be created in main()'s thread and merely handed in via closure (an earlier
        version of this wiring did exactly that: every relay DB call then raised
        sqlite3.ProgrammingError, silently caught by the relay's own broad error
        handling, so it spun forever never actually draining anything — caught by
        actually running the integration test against a real broker, not by
        inspection). Relay.run() itself calls flush() on stop, on this same thread, so
        no separate flush call is needed here."""
        conn = open_db(cfg.db_path)
        try:
            relay = Relay(
                fetch_pending=lambda limit: outbox_fetch_pending(conn, limit),
                mark_sent=lambda row_id, sent_at: outbox_mark_sent(conn, row_id, sent_at),
                mark_quarantined=lambda row_id, err: outbox_mark_quarantined(conn, row_id, err),
                record_failure=lambda row_id, err: outbox_record_failure(conn, row_id, err),
                validate=validator.validate_envelope_bytes,
                publish=publish,
                interval_s=cfg.outbox_poll_interval_ms / 1000.0,
                log=log,
            )
            relay_box["relay"] = relay
            relay.run(cycle_stop)
        finally:
            relay_box.pop("relay", None)
            conn.close()

    def run_consumer(cycle_stop: threading.Event) -> None:
        """The consumer thread's own entrypoint (Story 3.2). Uses its OWN Bus/AMQP
        connection, entirely separate from the one heartbeat/relay publish on:
        pika's BlockingChannel is documented (Bus.__init__) as unsafe for
        consume()/declare_dlq_topology() to share a channel with concurrent
        publish() calls from other threads -- consume()'s blocking generator and a
        concurrent basic_publish can interleave frames and hang the connection's
        I/O loop entirely, not just corrupt one frame (the same reason
        test_messaging_integration.py's park-then-observe assertion uses a separate
        observer Bus rather than reusing the producer's). Supervises its OWN
        reconnects via run_with_reconnect, scoped to cycle_stop -- independent of
        whether the main bus's connection is still healthy."""

        def connect_and_consume() -> None:
            # consumer_bus.close() must run on EVERY exit path from this point on,
            # including a failure inside connect()/declare_dlq_topology() itself --
            # otherwise a persistent topology mismatch leaks one connection per
            # run_with_reconnect retry attempt.
            consumer_bus = Bus(amqp_url, DRIVER_EXCHANGE)
            try:
                consumer_bus.connect()
                consumer_bus.declare_dlq_topology(
                    ConsumerOptions(
                        bindings=[
                            Binding(source_exchange=cfg.timing_exchange, routing_keys=[LAP_RECORDED_TYPE]),
                            Binding(source_exchange=cfg.identity_exchange, routing_keys=[IDENTITY_RESOLVED_TYPE]),
                        ],
                        queue_name=CONSUMER_QUEUE,
                        prefetch=cfg.consume_prefetch,
                        dlx_exchange=CONSUMER_DLX,
                    )
                )
                log.info(
                    "consumer queue declared", queue=CONSUMER_QUEUE, timingExchange=cfg.timing_exchange,
                    identityExchange=cfg.identity_exchange,
                )

                # Own SQLite connection, same cross-thread reason as run_relay's.
                conn = open_db(cfg.db_path)
                try:
                    handler = Handler(
                        validate=validator.validate_envelope_bytes,
                        conn=conn,
                        source=cfg.service_name,
                        policy=Policy(
                            max_attempts=cfg.dlq_max_attempts,
                            base_ms=cfg.dlq_retry_base_ms,
                            multiplier=cfg.dlq_retry_multiplier,
                            max_ms=cfg.dlq_retry_max_ms,
                        ),
                        log=log,
                        retry=consumer_bus.retry_to_dlx,
                        park=consumer_bus.park_to_dlx,
                        notify=notify_relay,
                    )
                    for delivery in consumer_bus.consume(CONSUMER_QUEUE, inactivity_timeout=_CONNECTION_POLL_S):
                        if cycle_stop.is_set():
                            return
                        if delivery is None:
                            continue
                        handler.process(delivery)
                finally:
                    conn.close()
            finally:
                consumer_bus.close()

        run_with_reconnect(connect_and_consume, cycle_stop.is_set, log)

    def connect_and_run() -> None:
        """One full connect-run-until-disconnected-or-stopped cycle. Returns cleanly
        when `stop` is set (run_with_reconnect then exits its own loop); raises
        AMQPConnectionError when a mid-run connection drop is detected (run_with_reconnect
        then backs off and calls this again, establishing a fresh Bus + fresh
        heartbeat/relay threads)."""
        bus = Bus(amqp_url, DRIVER_EXCHANGE)
        bus.connect()
        log.info("connected to bus", exchange=DRIVER_EXCHANGE)

        cycle_stop = threading.Event()  # scoped to THIS connection cycle only

        emitter = Emitter(
            interval_s=cfg.heartbeat_interval_ms / 1000.0,
            liveness_file=cfg.liveness_file,
            build=build_heartbeat,
            validate=validate_heartbeat,
            publish=bus.publish,
            log=log,
        )
        heartbeat_thread = threading.Thread(target=emitter.run, args=(cycle_stop,), name="heartbeat", daemon=True)
        heartbeat_thread.start()

        relay_thread = threading.Thread(target=run_relay, args=(bus.publish, cycle_stop), name="relay", daemon=True)
        relay_thread.start()

        consumer_thread = threading.Thread(target=run_consumer, args=(cycle_stop,), name="consumer", daemon=True)
        consumer_thread.start()

        connection_lost = False
        try:
            while not stop.wait(timeout=_CONNECTION_POLL_S):
                if not bus.is_connected():
                    log.error("bus connection lost")
                    connection_lost = True
                    break
        finally:
            cycle_stop.set()
            _join_and_log(heartbeat_thread, cfg.shutdown_timeout_ms, log, "heartbeat")
            _join_and_log(relay_thread, cfg.shutdown_timeout_ms, log, "relay")
            _join_and_log(consumer_thread, cfg.shutdown_timeout_ms, log, "consumer")
            bus.close()

        if connection_lost:
            raise AMQPConnectionError("bus connection closed")

    # Block indefinitely (retried across broker restarts by run_with_reconnect) until a
    # shutdown signal sets `stop` — this is the service's main loop, not a bounded wait.
    # ONLY once stop is set does connect_and_run's own bounded joins apply (the
    # shutdown_timeout_ms budget covers that drain, never the service's overall run
    # duration — a real bug caught here via a live Docker run: an earlier version used
    # shutdown_timeout_ms as this indefinite wait's own timeout, silently exiting the
    # whole service after 5s with no signal).
    run_with_reconnect(connect_and_run, stop.is_set, log)
    log.info("driver stopped")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
