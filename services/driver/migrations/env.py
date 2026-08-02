"""Alembic environment for Driver's private SQLite store. Reads DB_PATH from the
environment (the same variable driver.config.load_config reads) so the CLI
(`alembic upgrade head`) and the service run against the identical database file —
never invented separately. Falls back to the alembic.ini placeholder only when DB_PATH
is unset (local `alembic revision --autogenerate`-style tooling runs).
"""

import os
from logging.config import fileConfig

from alembic import context
from sqlalchemy import engine_from_config, pool

config = context.config

if config.config_file_name is not None:
    fileConfig(config.config_file_name)

db_path = os.environ.get("DB_PATH")
if db_path:
    config.set_main_option("sqlalchemy.url", f"sqlite:///{db_path}")

target_metadata = None  # raw-SQL migrations (op.execute) — no ORM models to autogenerate from


def run_migrations_offline() -> None:
    url = config.get_main_option("sqlalchemy.url")
    context.configure(url=url, target_metadata=target_metadata, literal_binds=True)
    with context.begin_transaction():
        context.run_migrations()


def run_migrations_online() -> None:
    connectable = engine_from_config(
        config.get_section(config.config_ini_section, {}),
        prefix="sqlalchemy.",
        poolclass=pool.NullPool,
    )
    with connectable.connect() as connection:
        context.configure(connection=connection, target_metadata=target_metadata)
        with context.begin_transaction():
            context.run_migrations()


if context.is_offline_mode():
    run_migrations_offline()
else:
    run_migrations_online()
