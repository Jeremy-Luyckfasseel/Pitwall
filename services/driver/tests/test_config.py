import pytest

from driver.config import ConfigError, load_config


def _env(**overrides):
    base = {
        "RABBITMQ_HOST": "rabbitmq",
        "RABBITMQ_PORT": "5672",
        "RABBITMQ_USER": "pitwall",
        "RABBITMQ_PASSWORD": "change-me",
    }
    base.update(overrides)
    return lambda key: base.get(key, "")


def test_missing_required_vars_fails_fast_listing_all():
    with pytest.raises(ConfigError) as exc:
        load_config(_env(RABBITMQ_HOST="", RABBITMQ_USER=""))
    assert "RABBITMQ_HOST" in str(exc.value)
    assert "RABBITMQ_USER" in str(exc.value)


def test_defaults_applied_when_optional_vars_absent():
    cfg = load_config(_env())
    assert cfg.rabbitmq_vhost == "/"
    assert cfg.heartbeat_interval_ms == 1000
    assert cfg.shutdown_timeout_ms == 5000
    assert cfg.log_level == "info"
    assert cfg.service_name == "driver"
    assert cfg.outbox_poll_interval_ms == 200
    assert cfg.consume_prefetch == 16
    assert cfg.dlq_max_attempts == 5
    assert cfg.dlq_retry_base_ms == 1000
    assert cfg.dlq_retry_multiplier == 2
    assert cfg.dlq_retry_max_ms == 60000
    assert cfg.instance_id  # minted when unset
    assert cfg.contract_dir == ""  # resolved by the validator when empty
    assert cfg.timing_exchange == "timing.events"
    assert cfg.identity_exchange == "identity.events"


def test_explicit_values_override_defaults():
    cfg = load_config(
        _env(
            RABBITMQ_VHOST="/custom",
            HEARTBEAT_INTERVAL_MS="2000",
            LOG_LEVEL="debug",
            SERVICE_NAME="driver",
            DB_PATH="/data/driver.db",
            LIVENESS_FILE="/tmp/pitwall-driver.live",
            CONTRACT_DIR="/contract",
            INSTANCE_ID="fixed-instance-1",
            TIMING_EXCHANGE="custom.timing.events",
            IDENTITY_EXCHANGE="custom.identity.events",
        )
    )
    assert cfg.rabbitmq_vhost == "/custom"
    assert cfg.heartbeat_interval_ms == 2000
    assert cfg.log_level == "debug"
    assert cfg.db_path == "/data/driver.db"
    assert cfg.liveness_file == "/tmp/pitwall-driver.live"
    assert cfg.contract_dir == "/contract"
    assert cfg.instance_id == "fixed-instance-1"
    assert cfg.timing_exchange == "custom.timing.events"
    assert cfg.identity_exchange == "custom.identity.events"


def test_non_positive_heartbeat_interval_rejected():
    with pytest.raises(ConfigError, match="HEARTBEAT_INTERVAL_MS"):
        load_config(_env(HEARTBEAT_INTERVAL_MS="0"))


def test_dlq_retry_max_below_base_rejected():
    with pytest.raises(ConfigError, match="DLQ_RETRY_MAX_MS"):
        load_config(_env(DLQ_RETRY_BASE_MS="5000", DLQ_RETRY_MAX_MS="1000"))


def test_none_returning_getenv_does_not_crash():
    """os.environ.get(key) — called with ONE positional arg, its own real signature —
    defaults to None for a missing key, unlike the dict-backed fake used elsewhere in
    this file (which defaults to ""). A real bug: found by actually running
    driver.main against a live broker (Task 6 smoke test), not by inspection."""
    base = {
        "RABBITMQ_HOST": "rabbitmq",
        "RABBITMQ_PORT": "5672",
        "RABBITMQ_USER": "pitwall",
        "RABBITMQ_PASSWORD": "change-me",
    }
    cfg = load_config(lambda key: base.get(key))  # returns None, not "", for absent keys
    assert cfg.shutdown_timeout_ms == 5000
    assert cfg.rabbitmq_vhost == "/"
    assert cfg.log_level == "info"
