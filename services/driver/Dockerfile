# Driver service image. Build context = repo ROOT (the service needs /contract,
# contract/codegen/python, and libs/py-pitwall, which live outside its dir —
# architecture §"Build & Dev Mechanics" #2):
#   docker build -f services/driver/Dockerfile -t pitwall-driver .
# (Compose sets build.context: . — see docker-compose.yml.)

# --- build stage --------------------------------------------------------------
# Wheels built once here; the runtime stage installs only the built wheels (no
# compilers/build backends in the final image).
FROM python:3.14-slim AS build
WORKDIR /src
COPY contract/codegen/python/ ./contract/codegen/python/
COPY libs/py-pitwall/ ./libs/py-pitwall/
COPY services/driver/ ./services/driver/

RUN pip wheel --no-cache-dir --wheel-dir /wheels \
    ./contract/codegen/python \
    ./libs/py-pitwall \
    ./services/driver

# --- runtime stage --------------------------------------------------------------
FROM python:3.14-slim
RUN useradd --create-home --uid 10001 pitwall

RUN --mount=type=bind,from=build,source=/wheels,target=/wheels \
    pip install --no-cache-dir --no-index --find-links=/wheels \
        pitwall-contract py-pitwall pitwall-driver

# The /contract tree is the single source of truth the service validates against at
# runtime (the generated wire DTOs vendored into the wheel above are a build-time-only
# artifact of it). COPY it into the image.
COPY contract /contract

# alembic.ini + migrations/ live at WORKDIR so driver.main's SERVICE_DIR default
# (os.getcwd()) finds them regardless of how the driver package was installed
# (see driver/main.py._default_service_dir doc comment).
WORKDIR /app
COPY services/driver/alembic.ini ./alembic.ini
COPY services/driver/migrations ./migrations
COPY services/driver/healthcheck.sh /usr/local/bin/healthcheck.sh
RUN chmod +x /usr/local/bin/healthcheck.sh

# Private SQLite store (outbox + inbox; racing-profile/lap-history tables land in
# Story 3.2+). Owned by the non-root runtime user so the service can create the DB on
# the mounted volume. The compose driver-data volume mounts over /data and keeps the
# data across restarts.
RUN mkdir -p /data && chown pitwall:pitwall /data /app
VOLUME ["/data"]

ENV CONTRACT_DIR=/contract \
    LIVENESS_FILE=/tmp/pitwall-driver.live \
    DB_PATH=/data/driver.db \
    PYTHONUNBUFFERED=1

USER pitwall

# Bus-only liveness — never an HTTP /health (ADR-0004, NFR18).
HEALTHCHECK --interval=10s --timeout=5s --start-period=15s --retries=3 \
  CMD ["/usr/local/bin/healthcheck.sh"]

ENTRYPOINT ["python", "-m", "driver.main"]
