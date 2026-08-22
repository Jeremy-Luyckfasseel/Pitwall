"""Driver's canonical-PR persistence (Story 3.4). Mirrors history.py's style: every
function runs inside the caller's transaction (pitwall.persistence.within_tx), mechanics
only, no domain rules.

Driver is the system of record for the all-time PR (FR11): history_best recomputes the
fastest lap authoritatively from driver_laps (Q&A Round 37 / Q37.4), and upsert_pr stores
the confirmed canonical value. The consumer decides WHETHER to write (only on a real
change) and folds in the event's claimed lapTimeMs for arrival-order robustness.
"""

from __future__ import annotations

import sqlite3


def history_best(conn: sqlite3.Connection, master_id: str) -> tuple[int, str, str] | None:
    """The authoritative recompute: the driver's fastest lap across ALL of driver_laps,
    with the session and wire time (`at`) of that lap. Returns (lap_time_ms, session_id,
    at) or None if the driver has no laps yet. Ties break by earliest `at` (the lap that
    set the record first)."""
    row = conn.execute(
        """
        SELECT lap_time_ms, session_id, at
        FROM driver_laps
        WHERE master_id = ?
        ORDER BY lap_time_ms ASC, at ASC
        LIMIT 1
        """,
        (master_id,),
    ).fetchone()
    if row is None:
        return None
    return int(row[0]), str(row[1]), str(row[2])


def read_pr(conn: sqlite3.Connection, master_id: str) -> int | None:
    """The currently-stored canonical best_lap_ms, or None if no PR is stored yet."""
    row = conn.execute(
        "SELECT best_lap_ms FROM driver_prs WHERE master_id = ?", (master_id,)
    ).fetchone()
    return None if row is None else int(row[0])


def upsert_pr(
    conn: sqlite3.Connection,
    master_id: str,
    best_lap_ms: int,
    session_id: str | None,
    set_at: str,
    now: str,
) -> None:
    """Stores/overwrites the canonical PR. On conflict, created_at is preserved and
    best_lap_ms/session_id/set_at/updated_at advance. The caller decides WHETHER to call
    this (only on a genuine change); this is pure mechanics."""
    conn.execute(
        """
        INSERT INTO driver_prs (master_id, best_lap_ms, session_id, set_at, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(master_id) DO UPDATE SET
            best_lap_ms = excluded.best_lap_ms,
            session_id  = excluded.session_id,
            set_at      = excluded.set_at,
            updated_at  = excluded.updated_at
        """,
        (master_id, best_lap_ms, session_id, set_at, now, now),
    )
