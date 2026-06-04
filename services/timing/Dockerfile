# Timing service image. Build context = repo ROOT (the service needs /contract,
# which lives outside its dir — architecture §"Build & Dev Mechanics" #2):
#   docker build -f services/timing/Dockerfile -t pitwall-timing .
# (Compose sets build.context: . — see docker-compose.yml.)

# --- build stage -------------------------------------------------------------
FROM golang:1.26.4 AS build
WORKDIR /src

# Cache deps on go.mod/go.sum only.
COPY services/timing/go.mod services/timing/go.sum ./
RUN go mod download

COPY services/timing/ ./
# Static binary so it runs on a minimal base.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/timing ./cmd/timing

# --- runtime stage -----------------------------------------------------------
# alpine (not distroless): the bus-only healthcheck is a POSIX sh script.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 pitwall

# The /contract tree is the single source of truth the service validates against
# at runtime (no codegen yet — that arrives in Epic 2). COPY it into the image.
COPY contract /contract
COPY --from=build /out/timing /usr/local/bin/timing
COPY services/timing/healthcheck.sh /usr/local/bin/healthcheck.sh
RUN chmod +x /usr/local/bin/healthcheck.sh

ENV CONTRACT_DIR=/contract \
    LIVENESS_FILE=/tmp/pitwall-timing.live

USER pitwall

# Bus-only liveness — never an HTTP /health (ADR-0004, NFR18).
HEALTHCHECK --interval=10s --timeout=5s --start-period=15s --retries=3 \
  CMD ["/usr/local/bin/healthcheck.sh"]

ENTRYPOINT ["/usr/local/bin/timing"]
