"""The shared structured-JSON logger for every Pitwall Python service (blueprint
§Observability). Every line carries timestamp, level, service and correlationId. It is
the service's single logging entrypoint — no bare print() to stdout (NFR20; enforced
by the hygiene guard test).
"""

from __future__ import annotations

import json
import sys
from datetime import UTC, datetime
from typing import IO, Any

_LEVELS = {"debug": 10, "info": 20, "warn": 30, "warning": 30, "error": 40}


def _wire_timestamp(now: datetime) -> str:
    """Exactly 3-digit milliseconds, literal 'Z' (never '+00:00') — the same format
    every language on the wire uses (AR9)."""
    return now.astimezone(UTC).strftime("%Y-%m-%dT%H:%M:%S.") + f"{now.microsecond // 1000:03d}Z"


class StructuredLogger:
    """A minimal, dependency-injected structured-JSON logger. Not built on the stdlib
    `logging` module's global registry — a plain instance, mirroring Go's `*slog.Logger`
    (constructed once per service, passed around explicitly, trivially unit-testable
    with a fake stream instead of monkeypatching global logging state)."""

    def __init__(self, service: str, correlation_id: str, level: str, stream: IO[str]):
        self._service = service
        self._correlation_id = correlation_id
        self._threshold = _LEVELS.get((level or "").strip().lower(), _LEVELS["info"])
        self._stream = stream

    def _log(self, level: str, message: str, **fields: Any) -> None:
        if _LEVELS[level] < self._threshold:
            return
        payload = {
            "timestamp": _wire_timestamp(datetime.now(UTC)),
            "level": level,
            "service": self._service,
            "correlationId": self._correlation_id,
            "message": message,
        }
        payload.update(fields)
        self._stream.write(json.dumps(payload) + "\n")

    def debug(self, message: str, **fields: Any) -> None:
        self._log("debug", message, **fields)

    def info(self, message: str, **fields: Any) -> None:
        self._log("info", message, **fields)

    def warn(self, message: str, **fields: Any) -> None:
        self._log("warn", message, **fields)

    def error(self, message: str, **fields: Any) -> None:
        self._log("error", message, **fields)


def new_logger(
    service: str,
    correlation_id: str,
    level: str = "info",
    stream: IO[str] | None = None,
) -> StructuredLogger:
    """Builds a JSON logger tagged with the service name and a lifecycle correlationId
    carried on every line. level is one of debug|info|warn|error (anything else falls
    back to info). stream defaults to stdout (injectable for tests)."""
    return StructuredLogger(service, correlation_id, level, stream if stream is not None else sys.stdout)
