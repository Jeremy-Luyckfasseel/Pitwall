#!/usr/bin/env sh
# Prod-compose validity gate (Story 1.12). Asserts that the merged production stack
#   docker compose -f docker-compose.yml -f docker-compose.prod.yml config
# obeys the ADR-0007 / Round-29 invariants BEFORE it ever reaches the VPS:
#   1. the merge resolves (with the prod-required env present);
#   2. timing + leaderboard run from PUBLIC GHCR images (ghcr.io/.../pitwall-<svc>:<ver>),
#      not a server build, and pull on every up (pull_policy: always) — the server never builds (ADR-0007);
#   3. the trackside board is published on LOOPBACK only (127.0.0.1) — no public surface (Q29.3);
#   4. neither compose file declares a top-level `version:` field (Compose Spec);
#   5. rabbitmq REFUSES to resolve on placeholder/missing creds (no secret-less prod boot, NFR20).
# Client-side only: `docker compose config` parses/merges/interpolates and needs NO daemon.
# POSIX sh so it runs identically in CI (ubuntu), git-bash (dev), and on the VPS.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT"

BASE="docker-compose.yml"
PROD="docker-compose.prod.yml"
OWNER="jeremy-luyckfasseel"   # GHCR namespace (public repo owner, lowercased) — NOT a secret
fail() { echo "check-prod-compose: FAIL — $1" >&2; exit 1; }

# --- 4. No top-level `version:` (Compose Spec). Ignore comments + nested keys. ---
for f in "$BASE" "$PROD"; do
  if grep -Eq '^version:' "$f"; then
    fail "$f declares a top-level 'version:' field — forbidden (Compose Specification)"
  fi
done

# --- 5. Negative: rabbitmq must fail fast without real creds. ---
# Provide image versions but DELIBERATELY omit RABBITMQ_USER/PASSWORD → the ${VAR:?...}
# guards in the prod overlay must make `config` exit non-zero.
if TIMING_VERSION="0.0.0-test" LEADERBOARD_VERSION="0.0.0-test" \
   RABBITMQ_USER="" RABBITMQ_PASSWORD="" \
   docker compose -f "$BASE" -f "$PROD" config >/dev/null 2>&1; then
  fail "prod config resolved WITHOUT real RabbitMQ creds — the fail-fast guard is missing (NFR20)"
fi

# --- 1. Positive: the merge resolves with the prod-required env present. ---
CONFIG="$(TIMING_VERSION="0.0.0-test" LEADERBOARD_VERSION="0.0.0-test" \
          RABBITMQ_USER="ci" RABBITMQ_PASSWORD="ci-not-a-real-secret" \
          docker compose -f "$BASE" -f "$PROD" config 2>/dev/null)" \
  || fail "merged prod config did not resolve with the prod env present"

# --- 2. timing + leaderboard run from public GHCR images + pull_policy: always. ---
for svc in timing leaderboard; do
  echo "$CONFIG" | grep -Eq "image:[[:space:]]+ghcr\.io/${OWNER}/pitwall-${svc}:" \
    || fail "service '${svc}' does not resolve to a ghcr.io/${OWNER}/pitwall-${svc}:<ver> image"
done
# pull_policy must appear for the pulled services (the server never builds — ADR-0007).
[ "$(echo "$CONFIG" | grep -c 'pull_policy: always')" -ge 2 ] \
  || fail "expected 'pull_policy: always' on both pulled services (server never builds — ADR-0007)"

# --- 3. The board is published on loopback only. ---
# Every published_port for the leaderboard board must bind 127.0.0.1 — never 0.0.0.0.
if echo "$CONFIG" | grep -Eq 'host_ip:[[:space:]]+0\.0\.0\.0'; then
  fail "a service publishes on 0.0.0.0 — the prod board must stay loopback-only (127.0.0.1, Q29.3)"
fi
echo "$CONFIG" | grep -Eq 'host_ip:[[:space:]]+127\.0\.0\.1' \
  || fail "could not confirm a 127.0.0.1 loopback publish for the board"

echo "check-prod-compose: OK — GHCR images, pull-only, loopback board, no version:, creds-fail-fast."
