"""Driver's racing_profiles table (Story 3.2): the minimal-profile safety net's own
persistence. Mirrors libs/py-pitwall/pitwall/persistence.py's style -- every function
runs inside the caller's transaction (pitwall.persistence.within_tx), no domain rules
here (that's driver.domain.profile), mechanics only.
"""

from __future__ import annotations

import sqlite3


def profile_exists(conn: sqlite3.Connection, master_id: str) -> bool:
    row = conn.execute("SELECT 1 FROM driver_profiles WHERE master_id = ?", (master_id,)).fetchone()
    return row is not None


def insert_minimal_profile(conn: sqlite3.Connection, master_id: str, at: str) -> None:
    """Creates an all-null-fields row for master_id if (and only if) one does not
    already exist. `INSERT ... WHERE NOT EXISTS` makes it structurally impossible for
    this function to ever touch an existing row's fields -- AC3's "Driver's write
    wins, never overwritten" holds by construction, not by caller discipline. A
    future field-EDITING path (out of this story's scope, Q&A Round 35/Q35.2) is a
    separate function; this one only ever creates."""
    conn.execute(
        """
        INSERT INTO driver_profiles (master_id, racing_number, kart_class, nickname, created_at, updated_at)
        SELECT ?, NULL, NULL, NULL, ?, ?
        WHERE NOT EXISTS (SELECT 1 FROM driver_profiles WHERE master_id = ?)
        """,
        (master_id, at, at, master_id),
    )
