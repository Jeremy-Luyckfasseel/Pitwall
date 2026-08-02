import sqlite3
from datetime import UTC, datetime

import pytest

from pitwall.erasure import Handler, Mode, guarded_create
from pitwall.persistence import open_db, outbox_fetch_pending

_SCHEMA = """
CREATE TABLE outbox (
    id TEXT PRIMARY KEY, routing_key TEXT NOT NULL, payload BLOB NOT NULL,
    status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT,
    created_at TEXT NOT NULL, sent_at TEXT
);
CREATE TABLE inbox (id TEXT PRIMARY KEY, type TEXT NOT NULL, processed_at TEXT NOT NULL);
CREATE TABLE racing_profile (master_id TEXT PRIMARY KEY, nickname TEXT);
CREATE TABLE tombstone (master_id TEXT PRIMARY KEY, tombstoned_at TEXT NOT NULL);
"""


MASTER_ID = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
REQUEST_ID = "aa11bb22-cc33-4dd4-8ee5-ff6677889900"
ENVELOPE_ID = "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f"
CORRELATION_ID = "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55"


@pytest.fixture()
def db(tmp_path) -> sqlite3.Connection:
    conn = open_db(str(tmp_path / "test.db"))
    conn.executescript(_SCHEMA)
    conn.execute("INSERT INTO racing_profile (master_id, nickname) VALUES (?, ?)", (MASTER_ID, "Speedy"))
    yield conn
    conn.close()


def _delete_slice(conn, master_id):
    conn.execute("DELETE FROM racing_profile WHERE master_id = ?", (master_id,))


def _write_tombstone(conn, master_id):
    conn.execute(
        "INSERT INTO tombstone (master_id, tombstoned_at) VALUES (?, ?)",
        (master_id, "2026-06-02T14:03:21.512Z"),
    )


def _enqueue_ack(conn, envelope):
    from pitwall.persistence import outbox_enqueue

    body = envelope.model_dump_json(by_alias=True, exclude_none=False).encode("utf-8")
    outbox_enqueue(conn, envelope.id, envelope.type, body, envelope.occurred_at)


def _is_tombstoned(conn, master_id) -> bool:
    return conn.execute("SELECT 1 FROM tombstone WHERE master_id = ?", (master_id,)).fetchone() is not None


def test_handle_deletes_tombstones_and_enqueues_ack(db):
    handler = Handler(
        db=db,
        service="driver",
        delete=_delete_slice,
        tombstone=_write_tombstone,
        enqueue=_enqueue_ack,
        now=lambda: datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC),
    )

    result = handler.handle(
        envelope_id=ENVELOPE_ID,
        correlation_id=CORRELATION_ID,
        request_id=REQUEST_ID,
        master_id=MASTER_ID,
    )

    assert result.duplicate is False
    assert result.ack.type == "privacy.erased"
    assert result.ack.causation_id == ENVELOPE_ID

    # slice deleted
    assert db.execute("SELECT 1 FROM racing_profile WHERE master_id = ?", (MASTER_ID,)).fetchone() is None
    # tombstone written
    assert _is_tombstoned(db, MASTER_ID)
    # ack durably enqueued (atomic with the delete+tombstone)
    rows = outbox_fetch_pending(db, 10)
    assert len(rows) == 1
    assert rows[0].routing_key == "privacy.erased"


def test_handle_is_idempotent_on_redelivery(db):
    handler = Handler(
        db=db, service="driver", delete=_delete_slice, tombstone=_write_tombstone, enqueue=_enqueue_ack,
        now=lambda: datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC),
    )

    first = handler.handle(ENVELOPE_ID, CORRELATION_ID, REQUEST_ID, MASTER_ID)
    assert first.duplicate is False

    second = handler.handle(ENVELOPE_ID, CORRELATION_ID, REQUEST_ID, MASTER_ID)
    assert second.duplicate is True
    assert second.ack is None

    # no double-enqueue
    assert len(outbox_fetch_pending(db, 10)) == 1


def test_handle_anonymized_mode(db):
    handler = Handler(
        db=db, service="billing", delete=_delete_slice, tombstone=_write_tombstone, enqueue=_enqueue_ack,
        mode=Mode.ANONYMIZED, now=lambda: datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC),
    )
    result = handler.handle(ENVELOPE_ID, CORRELATION_ID, REQUEST_ID, MASTER_ID)
    assert result.ack.data["mode"] == "anonymized"


def test_handle_rolls_back_everything_on_delete_failure(db):
    def failing_delete(conn, master_id):
        raise RuntimeError("db error")

    handler = Handler(
        db=db, service="driver", delete=failing_delete, tombstone=_write_tombstone, enqueue=_enqueue_ack,
        now=lambda: datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC),
    )
    with pytest.raises(RuntimeError):
        handler.handle(ENVELOPE_ID, CORRELATION_ID, REQUEST_ID, MASTER_ID)

    # nothing committed: slice still there, no tombstone, no outbox row, not marked seen
    assert db.execute("SELECT 1 FROM racing_profile WHERE master_id = ?", (MASTER_ID,)).fetchone() is not None
    assert not _is_tombstoned(db, MASTER_ID)
    assert outbox_fetch_pending(db, 10) == []


def test_guarded_create_blocks_when_tombstoned():
    created = []
    blocked = guarded_create(check=lambda mid: True, master_id="m-1", create=lambda: created.append("m-1"))
    assert blocked is True
    assert created == []


def test_guarded_create_allows_when_not_tombstoned():
    created = []
    blocked = guarded_create(check=lambda mid: False, master_id="m-1", create=lambda: created.append("m-1"))
    assert blocked is False
    assert created == ["m-1"]
