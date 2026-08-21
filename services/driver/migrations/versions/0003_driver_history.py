"""driver_laps + driver_session_summaries: lap-by-lap history and per-session
summaries (Story 3.3)

Revision ID: 0003
Revises: 0002
Create Date: 2026-08-21

- driver_laps: one row per counted lap. PK (master_id, session_id, lap_number)
  rejects a logically-duplicate lap; a UNIQUE index on source_event_id is the
  second line of idempotency defense (the inbox is the primary one) so a redelivered
  lap.recorded can never double-count even if a future code path bypasses the inbox
  guard (FR9 / NFR3).
- driver_session_summaries: one row per (master_id, session_id). Stores the
  per-driver session result carried by session.ended.summary[] (FR10). bestLapMs /
  lapCount are nullable -- a driver may have logged no counted lap.

master_id is intentionally NOT a foreign key to driver_profiles: SQLite FK
enforcement is off by default, and the consumer's minimal-profile safety net (Q36.3)
already guarantees the parent row exists before either table is written.
"""

from alembic import op

revision = "0003"
down_revision = "0002"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE driver_laps (
            master_id       TEXT    NOT NULL,
            session_id      TEXT    NOT NULL,
            lap_number      INTEGER NOT NULL,
            lap_time_ms     INTEGER NOT NULL,
            at              TEXT    NOT NULL,
            source_event_id TEXT    NOT NULL,
            created_at      TEXT    NOT NULL,
            PRIMARY KEY (master_id, session_id, lap_number)
        )
        """
    )
    op.execute(
        "CREATE UNIQUE INDEX ux_driver_laps_source_event_id ON driver_laps (source_event_id)"
    )
    op.execute(
        """
        CREATE TABLE driver_session_summaries (
            master_id   TEXT    NOT NULL,
            session_id  TEXT    NOT NULL,
            best_lap_ms INTEGER,
            lap_count   INTEGER,
            created_at  TEXT    NOT NULL,
            PRIMARY KEY (master_id, session_id)
        )
        """
    )


def downgrade() -> None:
    op.execute("DROP TABLE driver_session_summaries")
    op.execute("DROP INDEX ux_driver_laps_source_event_id")
    op.execute("DROP TABLE driver_laps")
