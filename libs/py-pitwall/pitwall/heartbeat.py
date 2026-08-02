"""Emits the 1 s liveness signal and maintains the liveness touch-file the Docker
healthcheck reads (blueprint §Liveness; ADR-0004 bus-only health). Dependencies are
injected so the loop is unit-testable without a broker. Mechanics only — the service
supplies its own build/validate/publish. Mirrors libs/go-pitwall/heartbeat/heartbeat.go,
using a stop Event instead of a context (idiomatic Python) since it runs in its own
thread alongside the service's main consume loop.
"""

from __future__ import annotations

import threading
from collections.abc import Callable
from datetime import datetime
from pathlib import Path
from typing import Any

from pitwall.envelope import format_wire_time


class Emitter:
    """Ticks every interval_s, building -> validating -> publishing a heartbeat and
    then touching liveness_file. An invalid heartbeat is logged + dropped (never
    published) and does NOT touch the liveness file."""

    def __init__(
        self,
        interval_s: float,
        liveness_file: str,
        build: Callable[[datetime], Any],
        validate: Callable[[Any], None],
        publish: Callable[[str, bytes], None],
        log: Any,
        now: Callable[[], datetime] | None = None,
    ):
        self._interval_s = interval_s
        self._liveness_file = liveness_file
        self._build = build
        self._validate = validate
        self._publish = publish
        self._log = log
        self._now = now or datetime.now

    def run(self, stop: threading.Event) -> None:
        """Blocks, emitting heartbeats until stop is set. Emits one heartbeat
        immediately, then on every tick. Returns on graceful stop."""
        self._emit_once(self._now())
        while not stop.wait(self._interval_s):
            self._emit_once(self._now())
        self._log.info("heartbeat loop stopped")

    def _emit_once(self, t: datetime) -> None:
        from pitwall.envelope import HEARTBEAT_ROUTING_KEY

        env = self._build(t)
        try:
            self._validate(env)
        except Exception as e:
            # Blueprint: invalid out -> log + drop, never publish.
            self._log.error("dropping invalid heartbeat (failed /contract validation)", error=str(e))
            return

        body = env.model_dump_json(by_alias=True, exclude_none=False).encode("utf-8")
        try:
            self._publish(HEARTBEAT_ROUTING_KEY, body)
        except Exception as e:
            self._log.error("failed to publish heartbeat", error=str(e))
            return

        try:
            _touch(self._liveness_file, t)
        except OSError as e:
            self._log.error("failed to update liveness file", error=str(e), file=self._liveness_file)
            return

        self._log.debug("heartbeat published", routingKey=HEARTBEAT_ROUTING_KEY)


def _touch(path: str, t: datetime) -> None:
    """Writes the timestamp to the liveness file, updating its mtime. The healthcheck
    treats a fresh mtime as proof the heartbeat loop (and thus the bus connection) is
    alive."""
    Path(path).write_text(format_wire_time(t) + "\n", encoding="utf-8")
