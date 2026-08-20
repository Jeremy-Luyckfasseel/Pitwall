"""Driver's racing_profiles table (Story 3.2): the minimal-profile safety net's own
persistence. Mirrors libs/py-pitwall/pitwall/persistence.py's style -- every function
runs inside the caller's transaction (pitwall.persistence.within_tx), no domain rules
here (that's driver.domain.profile), mechanics only.
"""

from __future__ import annotations

import sqlite3


def insert_minimal_profile(conn: sqlite3.Connection, master_id: str, at: str) -> bool:
    """Creates an all-null-fields row for master_id if (and only if) one does not
    already exist. `INSERT ... WHERE NOT EXISTS` makes it structurally impossible for
    this function to ever touch an existing row's fields -- AC3's "Driver's write
    wins, never overwritten" holds by construction, not by caller discipline. A
    future field-EDITING path (out of this story's scope, Q&A Round 35/Q35.2) is a
    separate function; this one only ever creates.

    Returns whether a row was actually inserted (True) or one already existed
    (False), read from the statement's own `rowcount` -- NOT from a preceding
    `profile_exists` check, which would be a separate SELECT racing against this
    INSERT under a concurrent writer. A caller that needs to know "did I just create
    this" (e.g. to decide whether to publish a fresh driver.profile_updated) must use
    this return value, not a check-then-insert of its own."""
    cur = conn.execute(
        """
        INSERT INTO driver_profiles (master_id, racing_number, kart_class, nickname, created_at, updated_at)
        SELECT ?, NULL, NULL, NULL, ?, ?
        WHERE NOT EXISTS (SELECT 1 FROM driver_profiles WHERE master_id = ?)
        """,
        (master_id, at, at, master_id),
    )
    return cur.rowcount == 1
