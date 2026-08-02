import threading
from datetime import UTC, datetime

import pytest
from pitwall.heartbeat import Emitter


class _FakeLog:
    def __init__(self):
        self.errors = []

    def info(self, *a, **k):
        pass

    def debug(self, *a, **k):
        pass

    def error(self, msg, **fields):
        self.errors.append((msg, fields))


class _FakeEnvelope:
    def __init__(self, data):
        self.data = data

    def model_dump_json(self, by_alias=True, exclude_none=False):
        return '{"type":"control.heartbeat"}'


def test_emits_immediately_then_touches_liveness_file(tmp_path):
    liveness = tmp_path / "live.touch"
    published = []
    stop = threading.Event()

    def build(t):
        stop.set()  # stop after the immediate emit
        return _FakeEnvelope({"at": "x"})

    emitter = Emitter(
        interval_s=1000,  # would never tick again within the test
        liveness_file=str(liveness),
        build=build,
        validate=lambda env: None,
        publish=lambda key, body: published.append((key, body)),
        log=_FakeLog(),
        now=lambda: datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC),
    )
    emitter.run(stop)

    assert len(published) == 1
    assert published[0][0] == "control.heartbeat"
    assert liveness.read_text().strip() == "2026-06-02T14:03:21.512Z"


def test_invalid_heartbeat_is_dropped_never_published(tmp_path):
    liveness = tmp_path / "live.touch"
    published = []
    stop = threading.Event()

    def validate(env):
        stop.set()
        raise ValueError("bad envelope")

    log = _FakeLog()
    emitter = Emitter(
        interval_s=1000,
        liveness_file=str(liveness),
        build=lambda t: _FakeEnvelope({}),
        validate=validate,
        publish=lambda key, body: published.append((key, body)),
        log=log,
        now=lambda: datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC),
    )
    emitter.run(stop)

    assert published == []
    assert not liveness.exists()
    assert any("invalid heartbeat" in msg for msg, _ in log.errors)


def test_publish_failure_does_not_touch_liveness_file(tmp_path):
    liveness = tmp_path / "live.touch"
    stop = threading.Event()

    def publish(key, body):
        stop.set()
        raise ConnectionError("broker unreachable")

    log = _FakeLog()
    emitter = Emitter(
        interval_s=1000,
        liveness_file=str(liveness),
        build=lambda t: _FakeEnvelope({}),
        validate=lambda env: None,
        publish=publish,
        log=log,
        now=lambda: datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC),
    )
    emitter.run(stop)

    assert not liveness.exists()
    assert any("failed to publish" in msg for msg, _ in log.errors)


def test_rejects_non_positive_interval():
    with pytest.raises(ValueError):
        Emitter(
            interval_s=0,
            liveness_file="unused",
            build=lambda t: _FakeEnvelope({}),
            validate=lambda env: None,
            publish=lambda key, body: None,
            log=_FakeLog(),
        )


def test_invalid_heartbeat_error_log_includes_exception_type(tmp_path):
    liveness = tmp_path / "live.touch"
    stop = threading.Event()

    def validate(env):
        stop.set()
        raise ValueError("bad envelope")

    log = _FakeLog()
    emitter = Emitter(
        interval_s=1000,
        liveness_file=str(liveness),
        build=lambda t: _FakeEnvelope({}),
        validate=validate,
        publish=lambda key, body: None,
        log=log,
        now=lambda: datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC),
    )
    emitter.run(stop)

    assert any(fields.get("errorType") == "ValueError" for _, fields in log.errors)


def test_ticks_multiple_times_before_stop(tmp_path):
    liveness = tmp_path / "live.touch"
    published = []
    stop = threading.Event()

    def publish(key, body):
        published.append((key, body))
        if len(published) >= 3:
            stop.set()

    emitter = Emitter(
        interval_s=0.01,
        liveness_file=str(liveness),
        build=lambda t: _FakeEnvelope({}),
        validate=lambda env: None,
        publish=publish,
        log=_FakeLog(),
        now=lambda: datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC),
    )
    emitter.run(stop)

    assert len(published) == 3
