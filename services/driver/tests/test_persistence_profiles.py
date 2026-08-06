"""Real-SQLite (no broker) tests for driver.persistence.profiles (Story 3.2 Task 4).
Proves the safety-net's core invariant by construction: insert_minimal_profile can
CREATE but structurally cannot overwrite an existing row (AC3, "Driver's write wins").
"""

from driver.persistence.profiles import insert_minimal_profile, profile_exists
from pitwall.persistence import open_db, within_tx

MASTER_ID = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"
AT = "2026-06-02T14:03:21.512Z"


def test_profile_exists_false_for_unknown_master_id(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        assert profile_exists(conn, MASTER_ID) is False
    finally:
        conn.close()


def test_insert_minimal_profile_creates_an_all_null_row(migrated_db_path):
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            created = insert_minimal_profile(conn, MASTER_ID, AT)
        assert created is True  # its own INSERT's effect, not a preceding SELECT (closes a TOCTOU)

        assert profile_exists(conn, MASTER_ID) is True
        row = conn.execute(
            "SELECT master_id, racing_number, kart_class, nickname, created_at, updated_at "
            "FROM driver_profiles WHERE master_id = ?",
            (MASTER_ID,),
        ).fetchone()
        assert tuple(row) == (MASTER_ID, None, None, None, AT, AT)
    finally:
        conn.close()


def test_insert_minimal_profile_is_a_no_op_when_the_row_already_exists(migrated_db_path):
    """AC3's precedence rule, enforced by construction: a second insert attempt for a
    masterId that already has a profile must not touch the existing row's fields --
    even if a caller passes a different `at`, the original created_at/updated_at
    (and any real values a future field-editor set) must survive untouched."""
    conn = open_db(migrated_db_path)
    try:
        with within_tx(conn):
            first = insert_minimal_profile(conn, MASTER_ID, AT)
        assert first is True

        # Simulate a real value having been set by some other, later path (not built
        # in this story -- just proves the guarantee holds regardless of row content).
        conn.execute("UPDATE driver_profiles SET nickname = 'Sofie-S' WHERE master_id = ?", (MASTER_ID,))

        later_at = "2026-06-02T15:00:00.000Z"
        with within_tx(conn):
            second = insert_minimal_profile(conn, MASTER_ID, later_at)
        assert second is False  # already existed -- its own INSERT's rowcount, not a stale SELECT

        row = conn.execute(
            "SELECT nickname, created_at, updated_at FROM driver_profiles WHERE master_id = ?",
            (MASTER_ID,),
        ).fetchone()
        assert tuple(row) == ("Sofie-S", AT, AT)  # untouched by the second insert attempt

        count = conn.execute("SELECT COUNT(*) FROM driver_profiles WHERE master_id = ?", (MASTER_ID,)).fetchone()[0]
        assert count == 1  # no duplicate row either
    finally:
        conn.close()
