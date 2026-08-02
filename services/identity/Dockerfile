# Identity service image. Build context = repo ROOT (the service needs /contract,
# which lives outside its dir — architecture §"Build & Dev Mechanics" #2):
#   docker build -f services/identity/Dockerfile -t pitwall-identity .
# (Compose sets build.context: . — see docker-compose.yml.)

# --- build stage -------------------------------------------------------------
FROM golang:1.26.4 AS build
# Build context = repo ROOT so the shared blueprint lib (libs/go-pitwall, pinned via a
# `replace` in go.mod) is in the context. GOWORK=off: the container build uses the
# per-service go.mod replace, NOT the repo-root go.work (which is dev-only). The repo
# layout is preserved under /src so `replace … => ../../libs/go-pitwall` resolves.
ENV GOWORK=off
WORKDIR /src
COPY libs/go-pitwall/ ./libs/go-pitwall/
COPY contract/codegen/go/ ./contract/codegen/go/

WORKDIR /src/services/identity
# Cache deps on go.mod/go.sum (the lib is replace-pinned local, fetched from context).
COPY services/identity/go.mod services/identity/go.sum ./
RUN go mod download

COPY services/identity/ ./
# Static binary so it runs on a minimal base.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/identity ./cmd/identity

# --- runtime stage -----------------------------------------------------------
# alpine (not distroless): the bus-only healthcheck is a POSIX sh script.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 pitwall

# The /contract tree is the single source of truth the service validates against at
# runtime (the generated wire DTOs vendored above are a build-time-only artifact of it).
# COPY it into the image.
COPY contract /contract
COPY --from=build /out/identity /usr/local/bin/identity
COPY services/identity/healthcheck.sh /usr/local/bin/healthcheck.sh
RUN chmod +x /usr/local/bin/healthcheck.sh

# Private SQLite store (identities + inbox + outbox). Owned by the non-root runtime
# user so the service can create the DB on the mounted volume. The compose
# identity-data volume mounts over /data and keeps the data across restarts.
RUN mkdir -p /data && chown pitwall:pitwall /data
VOLUME ["/data"]

ENV CONTRACT_DIR=/contract \
    LIVENESS_FILE=/tmp/pitwall-identity.live \
    DB_PATH=/data/identity.db

USER pitwall

# Bus-only liveness — never an HTTP /health (ADR-0004, NFR18).
HEALTHCHECK --interval=10s --timeout=5s --start-period=15s --retries=3 \
  CMD ["/usr/local/bin/healthcheck.sh"]

ENTRYPOINT ["/usr/local/bin/identity"]
