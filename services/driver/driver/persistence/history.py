"""Driver's lap-history and session-summary persistence (Story 3.3). Mirrors
profiles.py's style: every function runs inside the caller's transaction
(pitwall.persistence.within_tx), mechanics only, no domain rules.

Idempotency here is the SECOND line of defense -- the consumer's idempotent inbox
(dedupe on the envelope id) is the primary one. These functions additionally make a
double-count structurally impossible: driver_laps has a (master_id, session_id,
lap_number) PK plus a UNIQUE source_event_id index, and session-summary storage is
create-only.
"""

from __future__ import annotations

import sqlite3


def append_lap(
    conn: sqlite3.Connection,
    master_id: str,
    session_id: str,
    lap_number: int,
    lap_time_ms: int,
    at: str,
    source_event_id: str,
    created_at: str,
) -> bool:
    """Appends one lap to the driver's history. `INSERT ... ON CONFLICT DO NOTHING`
    means a redelivered lap (same source_event_id, caught by the UNIQUE index) or a
    logically-duplicate lap (same master_id+session_id+lap_number, caught by the PK)
    is silently ignored rather than double-counted (FR9 / NFR3).

    Returns whether a row was actually inserted (True) or a conflict skipped it
    (False), read from the statement's own `rowcount` -- so a caller never needs a
    preceding SELECT that would race the INSERT."""
    cur = conn.execute(
        """
        INSERT INTO driver_laps
            (master_id, session_id, lap_number, lap_time_ms, at, source_event_id, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT DO NOTHING
        """,
        (master_id, session_id, lap_number, lap_time_ms, at, source_event_id, created_at),
    )
    return cur.rowcount == 1


def upsert_session_summary(
    conn: sqlite3.Connection,
    master_id: str,
    session_id: str,
    best_lap_ms: int | None,
    lap_count: int | None,
    created_at: str,
) -> None:
    """Stores one per-(master_id, session_id) session summary (FR10). Create-only:
    `ON CONFLICT DO NOTHING` never overwrites an already-stored result. (The consumer's
    inbox already dedupes the whole session.ended, so a repeat here would be a logic
    error -- silently overwriting would hide it, so the first write wins by construction.)"""
    conn.execute(
        """
        INSERT INTO driver_session_summaries
            (master_id, session_id, best_lap_ms, lap_count, created_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT DO NOTHING
        """,
        (master_id, session_id, best_lap_ms, lap_count, created_at),
    )
