"""driver_prs: canonical all-time personal record per driver (Story 3.4)

Revision ID: 0004
Revises: 0003
Create Date: 2026-08-22

Driver is the system of record for the canonical all-time PR (FR11). One row per
driver (master_id PK). best_lap_ms is the fastest lap in ms, recomputed
authoritatively from driver_laps on each personal_record.broken (Q&A Round 37 /
Q37.4); session_id / set_at record which lap set it (set_at = that lap's wire time).
updated_at is the wire time of the last change. Timing keeps only a LOCAL cache of
this value, refreshed via driver.pr_updated.

master_id is intentionally NOT a foreign key to driver_profiles (SQLite FK
enforcement is off by default; the consumer's minimal-profile safety net already
guarantees the parent row exists before this table is written).
"""

from alembic import op

revision = "0004"
down_revision = "0003"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE driver_prs (
            master_id   TEXT    NOT NULL,
            best_lap_ms INTEGER NOT NULL,
            session_id  TEXT,
            set_at      TEXT    NOT NULL,
            created_at  TEXT    NOT NULL,
            updated_at  TEXT    NOT NULL,
            PRIMARY KEY (master_id)
        )
        """
    )


def downgrade() -> None:
    op.execute("DROP TABLE driver_prs")
