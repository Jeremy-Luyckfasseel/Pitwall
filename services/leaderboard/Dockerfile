# Leaderboard service image. Build context = repo ROOT (the service needs
# /contract, which lives outside its dir — architecture §"Build & Dev Mechanics"):
#   docker build -f services/leaderboard/Dockerfile -t pitwall-leaderboard .
# (Compose sets build.context: . — see docker-compose.yml.)

# --- web stage: build the Vite/React trackside board -------------------------
FROM node:24-alpine AS web
WORKDIR /app
# Cache deps on the manifest first.
COPY services/leaderboard/web/package.json ./web/
RUN cd web && npm install
COPY services/leaderboard/web/ ./web/
# Vite outDir is ../internal/web/dist (relative to web/), so the bundle lands at
# /app/internal/web/dist — exactly the //go:embed target the go stage overlays.
RUN cd web && npm run build

# --- build stage: compile the Go binary with the bundle embedded ------------
# Build context = repo ROOT so the shared blueprint lib (libs/go-pitwall, pinned via a
# `replace` in go.mod) is in the context. GOWORK=off: the container build uses the
# per-service go.mod replace, NOT the repo-root go.work (dev-only). The repo layout is
# preserved under /src so `replace … => ../../libs/go-pitwall` resolves.
FROM golang:1.26.4 AS build
ENV GOWORK=off
WORKDIR /src
COPY libs/go-pitwall/ ./libs/go-pitwall/

WORKDIR /src/services/leaderboard
COPY services/leaderboard/go.mod services/leaderboard/go.sum ./
RUN go mod download
COPY services/leaderboard/ ./
# Overlay the real built SPA over the committed .gitkeep placeholder so
# //go:embed all:dist picks up the actual bundle.
COPY --from=web /app/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/leaderboard ./cmd/leaderboard

# --- runtime stage ----------------------------------------------------------
# alpine (not distroless): the bus-only healthcheck is a POSIX sh script.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 pitwall

# The /contract tree is the single source of truth the service validates against
# at runtime (no codegen yet — Epic 2). COPY it into the image.
COPY contract /contract
COPY --from=build /out/leaderboard /usr/local/bin/leaderboard
COPY services/leaderboard/healthcheck.sh /usr/local/bin/healthcheck.sh
RUN chmod +x /usr/local/bin/healthcheck.sh

# Private SQLite store (inbox + standings). Owned by the non-root runtime user.
RUN mkdir -p /data && chown pitwall:pitwall /data
VOLUME ["/data"]

ENV CONTRACT_DIR=/contract \
    LIVENESS_FILE=/tmp/pitwall-leaderboard.live \
    DB_PATH=/data/leaderboard.db \
    HTTP_ADDR=:8080

EXPOSE 8080

USER pitwall

# Bus-only liveness — never an HTTP /health (ADR-0004, NFR18). The SSE/SPA HTTP
# server on :8080 is the live display, NOT a health endpoint.
HEALTHCHECK --interval=10s --timeout=5s --start-period=15s --retries=3 \
  CMD ["/usr/local/bin/healthcheck.sh"]

ENTRYPOINT ["/usr/local/bin/leaderboard"]
