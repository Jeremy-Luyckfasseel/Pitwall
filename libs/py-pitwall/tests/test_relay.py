import threading

import pytest
from pitwall.persistence import OutboxRow
from pitwall.relay import Relay


class _FakeLog:
    def __init__(self):
        self.errors = []
        self.warnings = []

    def info(self, *a, **k):
        pass

    def debug(self, *a, **k):
        pass

    def warning(self, msg, **fields):
        self.warnings.append((msg, fields))

    def error(self, msg, **fields):
        self.errors.append((msg, fields))


class _FakeStore:
    def __init__(self, pending):
        self.pending = list(pending)
        self.sent = []
        self.quarantined = []
        self.failed = []

    def fetch_pending(self, limit):
        return self.pending[:limit]

    def mark_sent(self, row_id, sent_at):
        self.sent.append(row_id)
        self.pending = [r for r in self.pending if r.id != row_id]

    def mark_quarantined(self, row_id, last_error):
        self.quarantined.append(row_id)
        self.pending = [r for r in self.pending if r.id != row_id]

    def record_failure(self, row_id, last_error):
        self.failed.append(row_id)


def _row(row_id: str) -> OutboxRow:
    return OutboxRow(
        id=row_id,
        routing_key="some.event",
        payload=b"{}",
        status="pending",
        attempts=0,
        last_error="",
        created_at="2026-06-02T14:03:21.512Z",
        sent_at="",
    )


def _relay(store, validate=lambda payload: None, publish=lambda key, body: None, interval_s=0.01):
    return Relay(
        fetch_pending=store.fetch_pending,
        mark_sent=store.mark_sent,
        mark_quarantined=store.mark_quarantined,
        record_failure=store.record_failure,
        validate=validate,
        publish=publish,
        interval_s=interval_s,
        log=_FakeLog(),
        now=lambda: "2026-06-02T14:03:22.000Z",
    )


def test_rejects_non_positive_interval():
    store = _FakeStore([])
    with pytest.raises(ValueError):
        Relay(
            fetch_pending=store.fetch_pending,
            mark_sent=store.mark_sent,
            mark_quarantined=store.mark_quarantined,
            record_failure=store.record_failure,
            validate=lambda payload: None,
            publish=lambda key, body: None,
            interval_s=0,
            log=_FakeLog(),
        )


def test_drain_once_publishes_and_marks_sent():
    store = _FakeStore([_row("a"), _row("b")])
    published = []
    relay = _relay(store, publish=lambda key, body: published.append((key, body)))

    sent = relay.drain_once()

    assert sent == 2
    assert store.sent == ["a", "b"]
    assert published == [("some.event", b"{}"), ("some.event", b"{}")]


def test_drain_once_quarantines_invalid_row_and_continues():
    store = _FakeStore([_row("bad"), _row("good")])
    published = []

    calls = {"n": 0}

    def validate_seq(payload):
        calls["n"] += 1
        if calls["n"] == 1:
            raise ValueError("schema mismatch")

    relay = _relay(store, validate=validate_seq, publish=lambda key, body: published.append((key, body)))

    sent = relay.drain_once()

    assert sent == 1
    assert store.quarantined == ["bad"]
    assert store.sent == ["good"]
    assert len(published) == 1


def test_drain_once_stops_batch_on_publish_failure_and_records_it():
    store = _FakeStore([_row("a"), _row("b")])

    def publish(key, body):
        raise ConnectionError("broker unreachable")

    relay = _relay(store, publish=publish)

    with pytest.raises(ConnectionError):
        relay.drain_once()

    assert store.failed == ["a"]
    assert store.sent == []
    assert "b" in [r.id for r in store.pending]  # never reached


def test_run_stops_on_stop_event():
    store = _FakeStore([])
    relay = _relay(store, interval_s=0.01)
    stop = threading.Event()
    stop.set()

    relay.run(stop)  # must return promptly, not hang


def test_run_drains_immediately_without_waiting_full_interval():
    store = _FakeStore([_row("a")])
    relay = _relay(store, interval_s=10.0)  # would never tick again within the test
    stop = threading.Event()

    def mark_sent_and_stop(row_id, sent_at):
        store.sent.append(row_id)
        store.pending = [r for r in store.pending if r.id != row_id]
        stop.set()

    store.mark_sent = mark_sent_and_stop
    relay = Relay(
        fetch_pending=store.fetch_pending,
        mark_sent=store.mark_sent,
        mark_quarantined=store.mark_quarantined,
        record_failure=store.record_failure,
        validate=lambda payload: None,
        publish=lambda key, body: None,
        interval_s=10.0,
        log=_FakeLog(),
        now=lambda: "2026-06-02T14:03:22.000Z",
    )

    relay.run(stop)

    assert store.sent == ["a"]


def test_kick_wakes_a_blocked_run_before_the_full_interval():
    store = _FakeStore([])
    relay = _relay(store, interval_s=5.0)
    stop = threading.Event()
    drains = []

    original_drain = relay.drain_once

    def counting_drain():
        drains.append(1)
        if len(drains) >= 2:
            stop.set()
        return original_drain()

    relay.drain_once = counting_drain

    t = threading.Thread(target=relay.run, args=(stop,))
    t.start()
    relay.kick()
    t.join(timeout=2.0)

    assert not t.is_alive()
    assert len(drains) >= 2


def test_flush_reports_sent_and_remaining():
    store = _FakeStore([_row("a"), _row("b")])
    relay = _relay(store, publish=lambda key, body: None)

    sent, remaining = relay.flush()

    assert sent == 2
    assert remaining == 0


def test_flush_reports_remaining_on_publish_failure():
    store = _FakeStore([_row("a"), _row("b")])

    def publish(key, body):
        raise ConnectionError("broker unreachable")

    relay = _relay(store, publish=publish)

    sent, remaining = relay.flush()

    assert sent == 0
    assert remaining == 2
