"""Real-process + real-broker helpers for the Python conformance runner (mirrors
tests/conformance/go/{broker,service}_integration_test.go's shape: build/launch the
real service, observe the real bus, assert observable outcomes — never a mock).
"""

from __future__ import annotations

import os
import re
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import IO

import pika
from pika.exceptions import AMQPConnectionError

from pitwall_conformance.scenario import repo_root

WIRE_TIME_PATTERN = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$")


def rabbit_env(params: pika.ConnectionParameters) -> dict[str, str]:
    """Splits pika ConnectionParameters into RABBITMQ_* env vars (mirrors the Go
    runner's rabbitEnv)."""
    return {
        "RABBITMQ_HOST": params.host,
        "RABBITMQ_PORT": str(params.port),
        "RABBITMQ_USER": params.credentials.username,
        "RABBITMQ_PASSWORD": params.credentials.password,
    }


@dataclass
class DriverProcess:
    proc: subprocess.Popen
    liveness_file: Path
    log_path: Path
    _log_file: IO[str]

    def log_text(self) -> str:
        try:
            return self.log_path.read_text(encoding="utf-8", errors="replace")
        except FileNotFoundError:
            return ""

    def close(self) -> None:
        """Closes the log file handle opened by start_driver. The process itself is
        NOT killed here — callers that need that call proc.kill()/proc.wait() first
        (this only releases the file handle, safe to call whether or not the process
        has already exited)."""
        self._log_file.close()


def start_driver(params: pika.ConnectionParameters, interval_ms: int, tmp_path: Path) -> DriverProcess:
    """Launches the REAL Driver process (`python -m driver.main`) against the broker
    described by params — the Python analogue of the Go runner's buildBinary+launch.
    Driver has no compiled binary (interpreted), so "build" is just resolving the
    installed `driver` package on PYTHONPATH; the process IS the real service module,
    not a stub."""
    driver_dir = repo_root() / "services" / "driver"
    db_path = tmp_path / "driver.db"
    liveness_file = tmp_path / "driver.live"
    log_path = tmp_path / "driver.log"

    env = dict(os.environ)
    env.update(rabbit_env(params))
    env.update(
        {
            "HEARTBEAT_INTERVAL_MS": str(interval_ms),
            "CONTRACT_DIR": str(repo_root() / "contract"),
            "DB_PATH": str(db_path),
            "LIVENESS_FILE": str(liveness_file),
            "SERVICE_NAME": "driver",
            "LOG_LEVEL": "info",
            "SERVICE_DIR": str(driver_dir),
            "PYTHONUNBUFFERED": "1",
        }
    )

    log_file = open(log_path, "w", encoding="utf-8")
    proc = subprocess.Popen(
        [sys.executable, "-m", "driver.main"],
        cwd=str(driver_dir),
        env=env,
        stdin=subprocess.DEVNULL,  # avoids a Windows handle-inheritance failure in
        # some harness/CI shells when stdin isn't explicitly given a real handle
        stdout=log_file,
        stderr=subprocess.STDOUT,
    )
    return DriverProcess(proc=proc, liveness_file=liveness_file, log_path=log_path, _log_file=log_file)


def observe_heartbeats(
    params: pika.ConnectionParameters, exchange: str, expect_source: str, min_count: int, window_s: float
) -> list[dict]:
    """Connects to the broker as an INDEPENDENT observer (never the service's own
    connection), binds a temporary exclusive queue to exchange on the
    control.heartbeat routing key, and collects deliveries until min_count is reached
    or the window elapses. Mirrors the Go runner's observeHeartbeats exactly (same
    exchange-declare/queue-declare/bind/consume shape) so both languages are proven
    against the identical wire mechanism, not just each against its own mock."""
    connection = pika.BlockingConnection(params)
    channel = connection.channel()
    channel.exchange_declare(exchange=exchange, exchange_type="topic", durable=True)
    result = channel.queue_declare(queue="", exclusive=True, auto_delete=True)
    queue_name = result.method.queue
    channel.queue_bind(queue=queue_name, exchange=exchange, routing_key="control.heartbeat")

    beats: list[dict] = []
    deadline = time.monotonic() + window_s
    try:
        for method, _properties, body in channel.consume(queue_name, auto_ack=True, inactivity_timeout=0.2):
            if method is None:  # inactivity_timeout tick — check the deadline
                if time.monotonic() >= deadline or len(beats) >= min_count:
                    break
                continue
            import json

            msg = json.loads(body)
            if msg.get("data", {}).get("service") != expect_source:
                continue  # a stray heartbeat from an unrelated process
            beats.append(msg)
            if len(beats) >= min_count or time.monotonic() >= deadline:
                break
    except AMQPConnectionError as e:
        raise AssertionError(
            f"broker connection lost while observing heartbeats (after {len(beats)} beats): {e}"
        ) from e
    finally:
        try:
            channel.cancel()
        except Exception as e:
            # Cleanup-only: the connection may already be closed/broken at this point
            # (e.g. after the AMQPConnectionError above), in which case cancel() itself
            # failing is expected and must not mask the real error. Log, don't swallow
            # silently, so an unexpected cleanup failure is still visible in test output.
            print(f"observe_heartbeats: channel.cancel() during cleanup failed: {e}", file=sys.stderr)
        connection.close()
    return beats


def assert_heartbeat_shape(msg: dict, want_service: str) -> None:
    assert msg.get("type") == "control.heartbeat", f"type = {msg.get('type')!r}, want control.heartbeat"
    assert msg.get("source") == want_service, f"source = {msg.get('source')!r}, want {want_service!r}"
    data = msg.get("data", {})
    assert data.get("service") == want_service, f"data.service = {data.get('service')!r}, want {want_service!r}"
    assert data.get("instanceId"), "data.instanceId is empty"
    at = data.get("at", "")
    assert WIRE_TIME_PATTERN.match(at), (
        f"data.at = {at!r} does not match the wire timestamp pattern (exactly 3-digit millis, literal Z)"
    )


def assert_graceful_shutdown(driver: DriverProcess, timeout_s: float = 10.0):
    """Sends SIGTERM to the real Driver process and asserts it exits with code 0
    within timeout_s (mirrors the Go runner's assertGracefulShutdown). On Windows,
    Python's signal delivery to a child process cannot express a real SIGTERM either
    (the same Go-stdlib limitation, one layer down in the OS) — skip with a clear
    reason rather than fail, exactly like the Go runner's platform-aware skip; the
    authoritative gate is Linux CI."""
    import signal

    if sys.platform == "win32":
        import pytest

        pytest.skip("SIGTERM delivery unsupported on windows — graceful-shutdown proof runs on the Linux CI gate")

    driver.proc.send_signal(signal.SIGTERM)
    try:
        exit_code = driver.proc.wait(timeout=timeout_s)
    except subprocess.TimeoutExpired:
        driver.proc.kill()
        raise AssertionError(f"process did not exit within {timeout_s}s of SIGTERM\nlog:\n{driver.log_text()}")
    if exit_code != 0:
        raise AssertionError(f"process exited {exit_code} after SIGTERM, want 0\nlog:\n{driver.log_text()}")
