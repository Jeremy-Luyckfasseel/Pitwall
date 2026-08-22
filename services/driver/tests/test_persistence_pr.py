"""Real-SQLite (no broker) tests for driver.persistence.pr (Story 3.4 Task 7).

Proves the canonical-PR recompute reads the fastest lap from driver_laps
authoritatively (Q37.4), and that upsert_pr stores/overwrites the canonical value.
"""

from driver.persistence.history import append_lap
from driver.persistence.pr import history_best, read_pr, upsert_pr
from pitwall.persistence import open_db, within_tx

MASTER_ID = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
OTHER_ID = "2b8e6d31-4f95-4e22-8bb3-6c7d5e4f3a10"
SESSION = "session-2026-05-31-evening-heat-3"
NOW = "2026-05-31T14:05:00.000Z"


def _add_lap(conn, n, ms, at, event_id, master_id=MASTER_ID, session=SESSION):
    with within_tx(conn):
        append_lap(conn, master_id, session, n, ms, at, event_id, NOW)


def test_history_best_none_when_no_laps(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        assert history_best(conn, MASTER_ID) is None
    finally:
        conn.close()


def test_history_best_returns_fastest_lap_with_session_and_at(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        _add_lap(conn, 1, 42318, "2026-05-31T14:01:00.000Z", "evt-1")
        _add_lap(conn, 2, 41980, "2026-05-31T14:02:00.000Z", "evt-2")  # fastest
        _add_lap(conn, 3, 42500, "2026-05-31T14:03:00.000Z", "evt-3")
        best = history_best(conn, MASTER_ID)
        assert best is not None
        lap_time_ms, session_id, at = best
        assert lap_time_ms == 41980
        assert session_id == SESSION
        assert at == "2026-05-31T14:02:00.000Z"
    finally:
        conn.close()


def test_history_best_is_per_driver(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        _add_lap(conn, 1, 41980, "2026-05-31T14:02:00.000Z", "evt-a", master_id=MASTER_ID)
        _add_lap(conn, 1, 39000, "2026-05-31T14:02:05.000Z", "evt-b", master_id=OTHER_ID)
        assert history_best(conn, MASTER_ID)[0] == 41980
        assert history_best(conn, OTHER_ID)[0] == 39000
    finally:
        conn.close()


def test_read_pr_none_then_value_after_upsert(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        assert read_pr(conn, MASTER_ID) is None
        with within_tx(conn):
            upsert_pr(conn, MASTER_ID, 41980, SESSION, "2026-05-31T14:02:00.000Z", NOW)
        assert read_pr(conn, MASTER_ID) == 41980
    finally:
        conn.close()


def test_upsert_pr_overwrites_and_preserves_created_at(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            upsert_pr(conn, MASTER_ID, 41980, SESSION, "2026-05-31T14:02:00.000Z", "2026-05-31T14:02:01.000Z")
        with within_tx(conn):
            upsert_pr(conn, MASTER_ID, 41000, SESSION, "2026-05-31T14:04:00.000Z", "2026-05-31T14:04:01.000Z")
        assert read_pr(conn, MASTER_ID) == 41000
        row = conn.execute(
            "SELECT best_lap_ms, set_at, created_at, updated_at FROM driver_prs WHERE master_id = ?",
            (MASTER_ID,),
        ).fetchone()
        assert row[0] == 41000
        assert row[1] == "2026-05-31T14:04:00.000Z"  # set_at advanced
        assert row[2] == "2026-05-31T14:02:01.000Z"  # created_at preserved
        assert row[3] == "2026-05-31T14:04:01.000Z"  # updated_at advanced
    finally:
        conn.close()
