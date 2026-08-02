"""Driver's entrypoint: runs its own Alembic migrations, connects to the bus, declares
its own durable exchange, emits a 1 s heartbeat, logs structured JSON, and shuts down
gracefully on SIGTERM/SIGINT (mirrors services/timing/cmd/timing/main.go's Story-1.3
shape). No domain logic yet (Story 3.2+) — this IS the skeleton the story delivers.
"""

from __future__ import annotations

import os
import signal
import threading

from alembic import command
from alembic.config import Config as AlembicConfig
from pitwall.envelope import new_heartbeat_envelope
from pitwall.heartbeat import Emitter
from pitwall.logging import new_logger
from pitwall.messaging import Bus
from pitwall.validate import Validator, resolve_contract_dir

from driver.config import ConfigError, load_config

DRIVER_EXCHANGE = "driver.events"


def _run_migrations(db_path: str, service_dir: str) -> None:
    os.environ["DB_PATH"] = db_path
    cfg = AlembicConfig(os.path.join(service_dir, "alembic.ini"))
    cfg.set_main_option("script_location", os.path.join(service_dir, "migrations"))
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
    bus = Bus(amqp_url, DRIVER_EXCHANGE)
    bus.connect()
    log.info("connected to bus", exchange=DRIVER_EXCHANGE)

    stop = threading.Event()

    def build_heartbeat(now):
        return new_heartbeat_envelope(
            service=cfg.service_name, instance_id=cfg.instance_id, correlation_id=cfg.instance_id, now=now
        )

    def validate_heartbeat(env):
        validator.validate_heartbeat(env.model_dump(mode="json", by_alias=True, exclude_none=False))

    def publish_heartbeat(routing_key: str, body: bytes) -> None:
        bus.publish(routing_key, body)

    emitter = Emitter(
        interval_s=cfg.heartbeat_interval_ms / 1000.0,
        liveness_file=cfg.liveness_file,
        build=build_heartbeat,
        validate=validate_heartbeat,
        publish=publish_heartbeat,
        log=log,
    )
    heartbeat_thread = threading.Thread(target=emitter.run, args=(stop,), name="heartbeat", daemon=True)
    heartbeat_thread.start()

    def handle_signal(signum, frame):
        log.info("shutdown signal received", signal=signum)
        stop.set()

    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)

    # Block indefinitely until a shutdown signal sets `stop` — this is the service's
    # main loop, not a bounded wait. ONLY once stop is set do we bound the wait for the
    # heartbeat thread to actually finish (the shutdown_timeout_ms budget covers that
    # drain, never the service's overall run duration — a real bug caught here via a
    # live Docker run: an earlier version used shutdown_timeout_ms as this indefinite
    # wait's own timeout, silently exiting the whole service after 5s with no signal).
    stop.wait()
    heartbeat_thread.join(timeout=cfg.shutdown_timeout_ms / 1000.0)
    bus.close()
    log.info("driver stopped")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
