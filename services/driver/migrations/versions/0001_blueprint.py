"""blueprint: outbox + inbox tables

Revision ID: 0001
Revises:
Create Date: 2026-08-02

The generic blueprint tables every Pitwall service owns privately (mechanics defined
by libs/go-pitwall's Go equivalent; libs/py-pitwall's pitwall.persistence module reads/
writes these exact column shapes). No domain tables yet — Driver's racing-profile/
lap-history tables land in Story 3.2+.
"""

from alembic import op

revision = "0001"
down_revision = None
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE outbox (
            id TEXT PRIMARY KEY,
            routing_key TEXT NOT NULL,
            payload BLOB NOT NULL,
            status TEXT NOT NULL DEFAULT 'pending',
            attempts INTEGER NOT NULL DEFAULT 0,
            last_error TEXT,
            created_at TEXT NOT NULL,
            sent_at TEXT
        )
        """
    )
    op.execute("CREATE INDEX ix_outbox_status_created_at ON outbox (status, created_at)")
    op.execute(
        """
        CREATE TABLE inbox (
            id TEXT PRIMARY KEY,
            type TEXT NOT NULL,
            processed_at TEXT NOT NULL
        )
        """
    )


def downgrade() -> None:
    op.execute("DROP TABLE inbox")
    op.execute("DROP TABLE outbox")
