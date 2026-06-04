#!/bin/sh
# Bus-only liveness check (NFR18, ADR-0004) — NEVER an HTTP /health.
# The heartbeat loop refreshes LIVENESS_FILE only after a successful publish to
# the bus, so a FRESH file proves the loop AND the broker connection are alive.
# Exit 0 = healthy, non-zero = unhealthy.
set -eu

LIVENESS_FILE="${LIVENESS_FILE:-/tmp/pitwall-timing.live}"
INTERVAL_MS="${HEARTBEAT_INTERVAL_MS:-1000}"
FACTOR="${HEALTH_FRESHNESS_FACTOR:-3}"

if [ ! -f "$LIVENESS_FILE" ]; then
  echo "unhealthy: liveness file missing ($LIVENESS_FILE)"
  exit 1
fi

now=$(date +%s)
# busybox stat (-c) on alpine; fall back to BSD stat (-f) elsewhere.
mtime=$(stat -c %Y "$LIVENESS_FILE" 2>/dev/null || stat -f %m "$LIVENESS_FILE")
age=$((now - mtime))

# Allow up to FACTOR heartbeat intervals of staleness (ceil to whole seconds, min 1s).
max_age=$(((INTERVAL_MS * FACTOR + 999) / 1000))
[ "$max_age" -lt 1 ] && max_age=1

if [ "$age" -le "$max_age" ]; then
  exit 0
fi
echo "unhealthy: liveness file stale (${age}s old > ${max_age}s allowed)"
exit 1
