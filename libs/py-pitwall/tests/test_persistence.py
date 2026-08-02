import sqlite3

import pytest

from pitwall.persistence import (
    inbox_has_seen,
    inbox_mark_seen,
    open_db,
    outbox_enqueue,
    outbox_fetch_pending,
    outbox_mark_quarantined,
    outbox_mark_sent,
    outbox_record_failure,
    within_tx,
)

_SCHEMA = """
CREATE TABLE outbox (
    id TEXT PRIMARY KEY,
    routing_key TEXT NOT NULL,
    payload BLOB NOT NULL,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TEXT NOT NULL,
    sent_at TEXT
);
CREATE TABLE inbox (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    processed_at TEXT NOT NULL
);
"""


@pytest.fixture()
def db(tmp_path) -> sqlite3.Connection:
    conn = open_db(str(tmp_path / "test.db"))
    conn.executescript(_SCHEMA)
    yield conn
    conn.close()


def test_open_db_applies_pragmas(db: sqlite3.Connection):
    assert db.execute("PRAGMA journal_mode").fetchone()[0].lower() == "wal"
    assert db.execute("PRAGMA foreign_keys").fetchone()[0] == 1


def test_within_tx_commits_on_success(db: sqlite3.Connection):
    with within_tx(db):
        outbox_enqueue(db, "id-1", "lap.recorded", b"{}", "2026-06-02T14:03:21.512Z")

    rows = outbox_fetch_pending(db, 10)
    assert len(rows) == 1
    assert rows[0].id == "id-1"
    assert rows[0].status == "pending"
    assert rows[0].attempts == 0


def test_within_tx_rolls_back_on_exception(db: sqlite3.Connection):
    with pytest.raises(ValueError):
        with within_tx(db):
            outbox_enqueue(db, "id-1", "lap.recorded", b"{}", "2026-06-02T14:03:21.512Z")
            raise ValueError("boom")

    assert outbox_fetch_pending(db, 10) == []


def test_outbox_fetch_pending_orders_oldest_first(db: sqlite3.Connection):
    with within_tx(db):
        outbox_enqueue(db, "id-2", "lap.recorded", b"{}", "2026-06-02T14:03:22.000Z")
        outbox_enqueue(db, "id-1", "lap.recorded", b"{}", "2026-06-02T14:03:21.000Z")

    rows = outbox_fetch_pending(db, 10)
    assert [r.id for r in rows] == ["id-1", "id-2"]


def test_outbox_mark_sent_excludes_from_pending(db: sqlite3.Connection):
    with within_tx(db):
        outbox_enqueue(db, "id-1", "lap.recorded", b"{}", "2026-06-02T14:03:21.512Z")
    with within_tx(db):
        outbox_mark_sent(db, "id-1", "2026-06-02T14:03:22.000Z")

    assert outbox_fetch_pending(db, 10) == []


def test_outbox_mark_quarantined_excludes_from_pending_and_bumps_attempts(db: sqlite3.Connection):
    with within_tx(db):
        outbox_enqueue(db, "id-1", "lap.recorded", b"{}", "2026-06-02T14:03:21.512Z")
    with within_tx(db):
        outbox_mark_quarantined(db, "id-1", "invalid against /contract")

    assert outbox_fetch_pending(db, 10) == []
    row = db.execute("SELECT status, attempts, last_error FROM outbox WHERE id = ?", ("id-1",)).fetchone()
    assert row["status"] == "quarantined"
    assert row["attempts"] == 1
    assert row["last_error"] == "invalid against /contract"


def test_outbox_record_failure_stays_pending(db: sqlite3.Connection):
    with within_tx(db):
        outbox_enqueue(db, "id-1", "lap.recorded", b"{}", "2026-06-02T14:03:21.512Z")
    with within_tx(db):
        outbox_record_failure(db, "id-1", "broker unreachable")

    rows = outbox_fetch_pending(db, 10)
    assert len(rows) == 1
    assert rows[0].attempts == 1
    assert rows[0].last_error == "broker unreachable"


def test_inbox_dedupe_round_trip(db: sqlite3.Connection):
    with within_tx(db):
        assert inbox_has_seen(db, "env-1") is False
        inbox_mark_seen(db, "env-1", "lap.recorded", "2026-06-02T14:03:21.512Z")

    with within_tx(db):
        assert inbox_has_seen(db, "env-1") is True
