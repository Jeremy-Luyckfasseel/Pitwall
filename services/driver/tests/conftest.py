"""Shared pytest fixtures for Driver's test suite."""

from pathlib import Path

import pytest
from alembic import command
from alembic.config import Config as AlembicConfig

_SERVICE_DIR = Path(__file__).resolve().parent.parent


@pytest.fixture()
def migrated_db_path(tmp_path) -> str:
    """A fresh SQLite file with every Alembic migration applied (outbox/inbox +
    driver_profiles), for tests that need real persistence without a broker."""
    db_path = str(tmp_path / "driver.db")
    cfg = AlembicConfig(str(_SERVICE_DIR / "alembic.ini"))
    cfg.set_main_option("script_location", str(_SERVICE_DIR / "migrations"))
    cfg.set_main_option("sqlalchemy.url", f"sqlite:///{db_path}")
    command.upgrade(cfg, "head")
    return db_path
