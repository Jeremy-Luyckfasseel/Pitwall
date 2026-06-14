#!/usr/bin/env bash
# Pitwall production deploy poller (Story 1.12 / ADR-0007 / Q29.1). PULL-based: no inbound SSH.
# Mirrors the proven AI-bot poller SHAPE (flock + git-fetch-tags + deployed-state marker), but it
# PULLS per-service images from PUBLIC GHCR and recreates ONLY the changed container — it NEVER builds
# on the server. Runs on a systemd timer (deploy/systemd/pitwall-deploy.{service,timer}).
#
# SAFETY — the VPS is shared with another live project: every docker/compose call is scoped to the
# `pitwall` project (-p pitwall); this script touches NOTHING outside it (no prune, no global stop).
set -uo pipefail

REPO_DIR="${PITWALL_REPO_DIR:-/root/pitwall}"
MARKER="${PITWALL_MARKER:-/root/pitwall.deployed_tags}"
LOG="${PITWALL_LOG:-/root/pitwall-deploy.log}"
LOCK="${PITWALL_LOCK:-/var/lock/pitwall-deploy.lock}"
COMPOSE=(docker compose -p pitwall -f docker-compose.yml -f docker-compose.prod.yml)
SERVICES=(timing leaderboard)   # services deployed by tag on this VPS

# A flock prevents a manual run colliding with the timer run.
exec 9>"$LOCK"
flock -n 9 || exit 0

cd "$REPO_DIR" || exit 0
touch "$MARKER"

# Refresh the repo (compose files + deploy scripts track main; images come from GHCR, not this tree).
# The VPS .env is gitignored, so it survives the reset.
git fetch origin --tags --prune --quiet 2>/dev/null || exit 0
git reset --hard origin/main --quiet 2>/dev/null || exit 0

# Which services have a newer released tag than what is deployed? (pure, tested logic)
selections="$(git tag -l '*-v*' | sh deploy/select-deploys.sh "$MARKER" 2>/dev/null || true)"
[ -n "$selections" ] || exit 0

{
  echo "=== $(date -u +%FT%TZ) poller run — selections: $(echo "$selections" | tr '\n' ',' ) ==="

  # Resolve the FULL desired version map (marker ∪ selections; selections win) and export every
  # <SVC>_VERSION up front — `docker compose` interpolates the whole file, so BOTH must be defined
  # even when only one service is being recreated.
  for svc in "${SERVICES[@]}"; do
    var="$(echo "$svc" | tr '[:lower:]' '[:upper:]')_VERSION"
    sel="$(echo "$selections" | awk -v s="$svc" '$1==s{print $2}')"
    cur="$(grep -E "^${svc}=" "$MARKER" 2>/dev/null | head -1 | cut -d= -f2- || true)"
    val="${sel:-$cur}"
    [ -n "$val" ] && export "$var=$val"
  done

  # Deploy each selected service: pull its immutable GHCR image, recreate ONLY that container.
  echo "$selections" | while read -r svc ver; do
    [ -n "$svc" ] || continue
    echo "--- deploying $svc -> $ver"
    if "${COMPOSE[@]}" pull "$svc" && "${COMPOSE[@]}" up -d --no-build "$svc"; then
      # Record success in the marker (atomic replace of this service's line).
      grep -v -E "^${svc}=" "$MARKER" 2>/dev/null > "$MARKER.tmp" || true
      echo "${svc}=${ver}" >> "$MARKER.tmp"
      mv "$MARKER.tmp" "$MARKER"
      echo "--- OK: $svc now $ver (only pitwall-$svc recreated)"
    else
      echo "!!! FAILED: $svc -> $ver (marker unchanged; will retry next tick)"
    fi
  done
  echo "=== $(date -u +%FT%TZ) done ==="
} >> "$LOG" 2>&1
