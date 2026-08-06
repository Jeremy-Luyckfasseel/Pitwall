"""Loads and validates the Driver service configuration from the environment
(12-factor; blueprint §config). Required values fail fast with a clear message — the
service never assumes a value (golden rule). Mirrors services/timing/internal/config/
config.go's shape and defaults.
"""

from __future__ import annotations

import uuid
from collections.abc import Callable
from dataclasses import dataclass

_REQUIRED_VARS = ["RABBITMQ_HOST", "RABBITMQ_PORT", "RABBITMQ_USER", "RABBITMQ_PASSWORD"]


class ConfigError(Exception):
    """Raised when required configuration is missing or invalid."""


@dataclass(frozen=True)
class Config:
    rabbitmq_host: str
    rabbitmq_port: str
    rabbitmq_user: str
    rabbitmq_password: str
    rabbitmq_vhost: str

    heartbeat_interval_ms: int
    shutdown_timeout_ms: int
    outbox_poll_interval_ms: int
    log_level: str
    service_name: str
    liveness_file: str
    db_path: str
    instance_id: str
    contract_dir: str  # empty = resolved by the validator (walk up from cwd)

    consume_prefetch: int
    dlq_max_attempts: int
    dlq_retry_base_ms: int
    dlq_retry_multiplier: int
    dlq_retry_max_ms: int

    timing_exchange: str  # Timing's exchange this consumer binds to (lap.recorded)
    identity_exchange: str  # Identity's exchange this consumer binds to (identity.resolved)


def _get(getenv: Callable[[str], str | None], key: str) -> str:
    """Coerces a getenv result to a string. Defensive against a caller passing
    os.environ.get directly: its default for a missing key is None (unlike Go's
    os.Getenv, which returns ""), and every call site here assumes a string comes
    back (see driver.main's own getenv wrapper — this is belt-and-suspenders, not a
    substitute for wrapping it correctly at the call site)."""
    v = getenv(key)
    return v if v is not None else ""


def _int_env(getenv: Callable[[str], str], key: str, default: int) -> int:
    raw = _get(getenv, key)
    if not raw.strip():
        return default
    try:
        return int(raw)
    except ValueError as e:
        raise ConfigError(f"{key} must be an integer, got {raw!r}") from e


def load_config(getenv: Callable[[str], str]) -> Config:
    """Resolves the configuration using the supplied getenv function (inject os.getenv
    in production; a dict-backed function in tests). Raises ConfigError listing every
    missing required variable rather than assuming defaults for them."""
    missing = [k for k in _REQUIRED_VARS if not _get(getenv, k).strip()]
    if missing:
        raise ConfigError(f"missing required environment variables: {', '.join(missing)}")

    heartbeat_interval_ms = _int_env(getenv, "HEARTBEAT_INTERVAL_MS", 1000)
    if heartbeat_interval_ms <= 0:
        raise ConfigError(f"HEARTBEAT_INTERVAL_MS must be a positive integer, got {heartbeat_interval_ms}")

    shutdown_timeout_ms = _int_env(getenv, "SHUTDOWN_TIMEOUT_MS", 5000)
    outbox_poll_interval_ms = _int_env(getenv, "OUTBOX_POLL_INTERVAL_MS", 200)
    if outbox_poll_interval_ms <= 0:
        raise ConfigError(f"OUTBOX_POLL_INTERVAL_MS must be a positive integer, got {outbox_poll_interval_ms}")

    consume_prefetch = _int_env(getenv, "CONSUME_PREFETCH", 16)
    if consume_prefetch <= 0:
        raise ConfigError(f"CONSUME_PREFETCH must be a positive integer, got {consume_prefetch}")

    dlq_max_attempts = _int_env(getenv, "DLQ_MAX_ATTEMPTS", 5)
    if dlq_max_attempts < 1:
        raise ConfigError(f"DLQ_MAX_ATTEMPTS must be >= 1, got {dlq_max_attempts}")

    dlq_retry_base_ms = _int_env(getenv, "DLQ_RETRY_BASE_MS", 1000)
    if dlq_retry_base_ms <= 0:
        raise ConfigError(f"DLQ_RETRY_BASE_MS must be a positive integer, got {dlq_retry_base_ms}")

    dlq_retry_multiplier = _int_env(getenv, "DLQ_RETRY_MULTIPLIER", 2)
    if dlq_retry_multiplier < 1:
        raise ConfigError(f"DLQ_RETRY_MULTIPLIER must be >= 1, got {dlq_retry_multiplier}")

    dlq_retry_max_ms = _int_env(getenv, "DLQ_RETRY_MAX_MS", 60000)
    if dlq_retry_max_ms < dlq_retry_base_ms:
        raise ConfigError(
            f"DLQ_RETRY_MAX_MS ({dlq_retry_max_ms}) must be >= DLQ_RETRY_BASE_MS ({dlq_retry_base_ms})"
        )

    instance_id = _get(getenv, "INSTANCE_ID").strip() or str(uuid.uuid7())

    return Config(
        rabbitmq_host=_get(getenv, "RABBITMQ_HOST"),
        rabbitmq_port=_get(getenv, "RABBITMQ_PORT"),
        rabbitmq_user=_get(getenv, "RABBITMQ_USER"),
        rabbitmq_password=_get(getenv, "RABBITMQ_PASSWORD"),
        rabbitmq_vhost=_get(getenv, "RABBITMQ_VHOST").strip() or "/",
        heartbeat_interval_ms=heartbeat_interval_ms,
        shutdown_timeout_ms=shutdown_timeout_ms,
        outbox_poll_interval_ms=outbox_poll_interval_ms,
        log_level=_get(getenv, "LOG_LEVEL").strip() or "info",
        service_name=_get(getenv, "SERVICE_NAME").strip() or "driver",
        liveness_file=_get(getenv, "LIVENESS_FILE").strip() or "/tmp/pitwall-driver.live",
        db_path=_get(getenv, "DB_PATH").strip() or "/data/driver.db",
        instance_id=instance_id,
        contract_dir=_get(getenv, "CONTRACT_DIR").strip(),
        consume_prefetch=consume_prefetch,
        dlq_max_attempts=dlq_max_attempts,
        dlq_retry_base_ms=dlq_retry_base_ms,
        dlq_retry_multiplier=dlq_retry_multiplier,
        dlq_retry_max_ms=dlq_retry_max_ms,
        timing_exchange=_get(getenv, "TIMING_EXCHANGE").strip() or "timing.events",
        identity_exchange=_get(getenv, "IDENTITY_EXCHANGE").strip() or "identity.events",
    )
