"""driver_profiles: the racing-profile table (Story 3.2)

Revision ID: 0002
Revises: 0001
Create Date: 2026-08-06

All three racing fields start nullable -- the minimal-profile safety net (FR12)
never invents a value, it only ever creates the row.
"""

from alembic import op

revision = "0002"
down_revision = "0001"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE driver_profiles (
            master_id TEXT PRIMARY KEY,
            racing_number TEXT,
            kart_class TEXT,
            nickname TEXT,
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL
        )
        """
    )


def downgrade() -> None:
    op.execute("DROP TABLE driver_profiles")
