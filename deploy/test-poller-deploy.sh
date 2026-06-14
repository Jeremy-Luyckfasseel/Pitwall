#!/usr/bin/env bash
# Behaviour test for deploy/pitwall-deploy-poller.sh (Story 1.12, Task 3), with git/docker/flock
# STUBBED so it runs anywhere (no VPS, no daemon, no real fetch/reset). It pins the two correctness
# properties that would otherwise only surface on the VPS:
#   A. on a FIRST deploy (empty marker) BOTH <SVC>_VERSION are exported before any compose call
#      (compose interpolates the whole file → a single-service pull must still resolve the other);
#   B. only the SELECTED services are pulled + recreated, and the marker is updated to the deployed tag.
set -uo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SANDBOX="$(mktemp -d)"
trap 'rm -rf "$SANDBOX"' EXIT

# --- a fake repo the poller will cd into (needs the compose files + the selection logic) ---
REPO="$SANDBOX/repo"
mkdir -p "$REPO/deploy" "$REPO/scripts"
cp "$ROOT/docker-compose.yml" "$ROOT/docker-compose.prod.yml" "$REPO/"
cp "$ROOT/deploy/select-deploys.sh" "$REPO/deploy/"
cp "$ROOT/scripts/parse-service-tag.sh" "$REPO/scripts/"

# --- stubbed git / docker / flock on PATH ---
BIN="$SANDBOX/bin"; mkdir -p "$BIN"
DLOG="$SANDBOX/docker-calls.log"
cat > "$BIN/git" <<EOF
#!/bin/sh
# 'git tag -l <glob>' returns the fixture tags; everything else (fetch/reset) is a no-op success.
case "\$1 \$2" in
  "tag -l") printf '%s\n' "timing-v0.1.0" "leaderboard-v0.2.0" ;;
  *) exit 0 ;;
esac
EOF
cat > "$BIN/docker" <<EOF
#!/bin/sh
# Record the action+service and prove BOTH version vars are visible at call time.
action=""; svc=""
for a in "\$@"; do case "\$a" in pull|up) action="\$a";; esac; svc="\$a"; done
echo "docker \$action \$svc | TIMING_VERSION=\${TIMING_VERSION:-UNSET} LEADERBOARD_VERSION=\${LEADERBOARD_VERSION:-UNSET}" >> "$DLOG"
exit 0
EOF
cat > "$BIN/flock" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$BIN/git" "$BIN/docker" "$BIN/flock"

# --- run the poller against the sandbox (empty marker = first deploy) ---
MARKER="$SANDBOX/marker"; : > "$MARKER"
PATH="$BIN:$PATH" \
  PITWALL_REPO_DIR="$REPO" PITWALL_MARKER="$MARKER" \
  PITWALL_LOG="$SANDBOX/poller.log" PITWALL_LOCK="$SANDBOX/lock" \
  bash "$ROOT/deploy/pitwall-deploy-poller.sh"

fails=0
calls="$(cat "$DLOG" 2>/dev/null || true)"

# B. both selected services pulled + recreated
echo "$calls" | grep -q "docker pull timing "        || { echo "FAIL: timing was not pulled"; fails=$((fails+1)); }
echo "$calls" | grep -q "docker up timing "          || { echo "FAIL: timing was not recreated"; fails=$((fails+1)); }
echo "$calls" | grep -q "docker pull leaderboard "   || { echo "FAIL: leaderboard was not pulled"; fails=$((fails+1)); }

# A. both version vars defined on EVERY compose call (the first-deploy interpolation guard)
if echo "$calls" | grep -q "UNSET"; then
  echo "FAIL: a *_VERSION was UNSET during a compose call (first-deploy interpolation would break):"; echo "$calls"; fails=$((fails+1))
fi
echo "$calls" | grep -q "TIMING_VERSION=0.1.0"        || { echo "FAIL: TIMING_VERSION not resolved to 0.1.0"; fails=$((fails+1)); }
echo "$calls" | grep -q "LEADERBOARD_VERSION=0.2.0"   || { echo "FAIL: LEADERBOARD_VERSION not resolved to 0.2.0"; fails=$((fails+1)); }

# marker updated to the deployed tags
grep -q "^timing=0.1.0$"      "$MARKER" || { echo "FAIL: marker missing timing=0.1.0"; fails=$((fails+1)); }
grep -q "^leaderboard=0.2.0$" "$MARKER" || { echo "FAIL: marker missing leaderboard=0.2.0"; fails=$((fails+1)); }

if [ "$fails" -eq 0 ]; then echo "test-poller-deploy: OK"; else echo "test-poller-deploy: $fails FAILURE(S)" >&2; exit 1; fi
