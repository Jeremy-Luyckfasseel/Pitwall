"""Shared database blueprint mechanics every Pitwall Python service reuses: opening a
private SQLite store with the standard connection pragmas, the within_tx transaction
seam, the transactional outbox store, and the idempotent inbox. Mechanics ONLY — no
domain tables or projections (those live in each service, defined by its own Alembic
migrations). Mirrors libs/go-pitwall/persistence/{db,outbox,inbox}.go.
"""

from __future__ import annotations

import sqlite3
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass


def open_db(db_path: str) -> sqlite3.Connection:
    """Opens (creating if absent) the SQLite database at db_path and applies the
    standard connection pragmas: WAL journal (reader/writer concurrency), a busy
    timeout (wait rather than fail under contention), and foreign-key enforcement.
    Does NOT run migrations — the caller runs its own Alembic migrations (service-owned).
    """
    conn = sqlite3.connect(db_path, timeout=5.0, isolation_level=None)
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA busy_timeout=5000")
    conn.execute("PRAGMA foreign_keys=ON")
    conn.row_factory = sqlite3.Row
    return conn


@contextmanager
def within_tx(conn: sqlite3.Connection) -> Iterator[sqlite3.Connection]:
    """Runs the wrapped block inside a single transaction, committing on success and
    rolling back on any exception. This is the seam the outbox's atomicity (and the
    inbox's dedupe-with-write) rests on: a service writes its domain state AND its
    outbox/inbox row through the same transaction, so the two commit together or not
    at all."""
    conn.execute("BEGIN")
    try:
        yield conn
    except BaseException:
        conn.execute("ROLLBACK")
        raise
    else:
        conn.execute("COMMIT")


# --- Outbox -------------------------------------------------------------------------


@dataclass(frozen=True)
class OutboxRow:
    id: str  # = envelope id
    routing_key: str  # = envelope type; the key published on the service's exchange
    payload: bytes  # the full, already-marshalled envelope JSON
    status: str  # pending | sent | quarantined
    attempts: int
    last_error: str
    created_at: str  # wire-format timestamp
    sent_at: str  # empty until sent


def outbox_enqueue(conn: sqlite3.Connection, row_id: str, routing_key: str, payload: bytes, created_at: str) -> None:
    """Inserts a pending outbox row using the caller's (open) transaction, so the row
    commits together with whatever domain state the same transaction wrote. Always
    inserted pending with zero attempts."""
    conn.execute(
        "INSERT INTO outbox (id, routing_key, payload, status, attempts, created_at) "
        "VALUES (?, ?, ?, 'pending', 0, ?)",
        (row_id, routing_key, payload, created_at),
    )


def outbox_fetch_pending(conn: sqlite3.Connection, limit: int) -> list[OutboxRow]:
    """Returns up to limit pending rows, oldest-first (created_at, then id as a stable
    tie-breaker), so the relay publishes in production order."""
    cur = conn.execute(
        "SELECT id, routing_key, payload, status, attempts, COALESCE(last_error, ''), created_at, "
        "COALESCE(sent_at, '') FROM outbox WHERE status = 'pending' "
        "ORDER BY created_at ASC, id ASC LIMIT ?",
        (limit,),
    )
    return [OutboxRow(*r) for r in cur.fetchall()]


def outbox_mark_sent(conn: sqlite3.Connection, row_id: str, sent_at: str) -> None:
    conn.execute("UPDATE outbox SET status = 'sent', sent_at = ? WHERE id = ?", (sent_at, row_id))


def outbox_mark_quarantined(conn: sqlite3.Connection, row_id: str, last_error: str) -> None:
    """Terminally quarantines a row that could not be validated against /contract: it
    is never published and never retried. This is a producer-side quarantine (a local
    status), distinct from the consumer-side RabbitMQ DLQ/parking topology."""
    conn.execute(
        "UPDATE outbox SET status = 'quarantined', attempts = attempts + 1, last_error = ? WHERE id = ?",
        (last_error, row_id),
    )


def outbox_record_failure(conn: sqlite3.Connection, row_id: str, last_error: str) -> None:
    """Notes a transient publish failure (broker unreachable / nack): the row stays
    pending and is retried on a later tick."""
    conn.execute(
        "UPDATE outbox SET attempts = attempts + 1, last_error = ? WHERE id = ?",
        (last_error, row_id),
    )


# --- Idempotent inbox -----------------------------------------------------------------
# Dedupe on envelope id so an at-least-once redelivery is a safe no-op (M6). Both
# helpers run inside the CALLER's transaction so the dedupe check + the read-model
# write + the inbox insert commit together.


def inbox_has_seen(conn: sqlite3.Connection, envelope_id: str) -> bool:
    row = conn.execute("SELECT 1 FROM inbox WHERE id = ?", (envelope_id,)).fetchone()
    return row is not None


def inbox_mark_seen(conn: sqlite3.Connection, envelope_id: str, event_type: str, processed_at: str) -> None:
    conn.execute(
        "INSERT INTO inbox (id, type, processed_at) VALUES (?, ?, ?)",
        (envelope_id, event_type, processed_at),
    )
