"""Real-SQLite (no broker) tests for driver.persistence.history (Story 3.3 Task 4).
Proves lap append is idempotent by construction (PK + UNIQUE source_event_id) and
that session-summary storage is create-only (never a silent overwrite).
"""

from driver.persistence.history import append_lap, upsert_session_summary
from pitwall.persistence import open_db, within_tx

MASTER_ID = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
OTHER_ID = "2b8e6d31-4f95-4e22-8bb3-6c7d5e4f3a10"
SESSION = "session-2026-05-31-evening-heat-3"
AT = "2026-05-31T14:03:21.500Z"
CREATED = "2026-05-31T14:03:22.000Z"


def _laps(conn, master_id=MASTER_ID):
    return conn.execute(
        "SELECT lap_number, lap_time_ms, at, source_event_id FROM driver_laps "
        "WHERE master_id = ? AND session_id = ? ORDER BY lap_number",
        (master_id, SESSION),
    ).fetchall()


def test_append_lap_inserts_a_row_and_reports_created(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            created = append_lap(conn, MASTER_ID, SESSION, 7, 42318, AT, "evt-1", CREATED)
        assert created is True
        rows = _laps(conn)
        assert len(rows) == 1
        assert tuple(rows[0]) == (7, 42318, AT, "evt-1")
    finally:
        conn.close()


def test_append_lap_same_source_event_id_is_ignored_not_double_counted(migrated_db_path):
    """FR9/NFR3 second line of defense: a redelivered lap.recorded (same envelope id
    -> same source_event_id) must not create a second row, even for the same lap."""
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            first = append_lap(conn, MASTER_ID, SESSION, 7, 42318, AT, "evt-1", CREATED)
        assert first is True
        with within_tx(conn):
            again = append_lap(conn, MASTER_ID, SESSION, 7, 42318, AT, "evt-1", CREATED)
        assert again is False  # ignored, not inserted
        assert len(_laps(conn)) == 1
    finally:
        conn.close()


def test_append_lap_same_lap_number_different_event_is_ignored(migrated_db_path):
    """The (master_id, session_id, lap_number) PK rejects a logically-duplicate lap
    even if it arrived under a different envelope id."""
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            append_lap(conn, MASTER_ID, SESSION, 7, 42318, AT, "evt-1", CREATED)
        with within_tx(conn):
            dup = append_lap(conn, MASTER_ID, SESSION, 7, 99999, AT, "evt-2", CREATED)
        assert dup is False
        rows = _laps(conn)
        assert len(rows) == 1
        assert rows[0][1] == 42318  # original lap_time_ms untouched
    finally:
        conn.close()


def test_append_lap_distinct_laps_accumulate(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            append_lap(conn, MASTER_ID, SESSION, 7, 42318, AT, "evt-1", CREATED)
            append_lap(conn, MASTER_ID, SESSION, 8, 41980, AT, "evt-2", CREATED)
        rows = _laps(conn)
        assert [r[0] for r in rows] == [7, 8]
    finally:
        conn.close()


def test_upsert_session_summary_stores_a_row(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            upsert_session_summary(conn, MASTER_ID, SESSION, 41980, 12, CREATED)
        row = conn.execute(
            "SELECT best_lap_ms, lap_count, created_at FROM driver_session_summaries "
            "WHERE master_id = ? AND session_id = ?",
            (MASTER_ID, SESSION),
        ).fetchone()
        assert tuple(row) == (41980, 12, CREATED)
    finally:
        conn.close()


def test_upsert_session_summary_allows_null_stats(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            upsert_session_summary(conn, MASTER_ID, SESSION, None, None, CREATED)
        row = conn.execute(
            "SELECT best_lap_ms, lap_count FROM driver_session_summaries "
            "WHERE master_id = ? AND session_id = ?",
            (MASTER_ID, SESSION),
        ).fetchone()
        assert tuple(row) == (None, None)
    finally:
        conn.close()


def test_upsert_session_summary_does_not_overwrite_an_existing_row(migrated_db_path):
    """The inbox already dedupes the whole session.ended; a repeat storing a
    different value would be a logic error, so storage is create-only (no silent
    overwrite of the first-stored result)."""
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            upsert_session_summary(conn, MASTER_ID, SESSION, 41980, 12, CREATED)
        with within_tx(conn):
            upsert_session_summary(conn, MASTER_ID, SESSION, 99999, 99, "2026-05-31T15:00:00.000Z")
        row = conn.execute(
            "SELECT best_lap_ms, lap_count, created_at FROM driver_session_summaries "
            "WHERE master_id = ? AND session_id = ?",
            (MASTER_ID, SESSION),
        ).fetchone()
        assert tuple(row) == (41980, 12, CREATED)  # first write survives
    finally:
        conn.close()
