#!/usr/bin/env sh
# Test-first spec for deploy/select-deploys.sh (Story 1.12, Task 3) — the poller's pure
# selection logic, isolated from git/docker/the VPS so it is unit-testable.
# Contract: reads candidate tags on stdin + a deployed-state marker file ("service=version" lines),
# and prints "<service> <version>" for EACH service whose newest well-formed `‹svc›-vX.Y.Z` tag
# differs from what is recorded as deployed. Newest = highest semver. Malformed tags are ignored.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SEL="$ROOT/deploy/select-deploys.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fails=0

# run <name> <marker-contents> <tags-multiline> <expected-output>
run() {
  name="$1"; printf '%s' "$2" > "$TMP/marker";
  got="$(printf '%s\n' "$3" | sh "$SEL" "$TMP/marker" 2>/dev/null | sort || true)"
  exp="$(printf '%s' "$4" | sort)"
  if [ "$got" != "$exp" ]; then
    echo "FAIL [$name]: expected [$(echo "$exp" | tr '\n' '|')] got [$(echo "$got" | tr '\n' '|')]"; fails=$((fails+1))
  fi
}

# One service advanced (timing 0.1.0 → 0.1.1); leaderboard unchanged → only timing emitted.
run "one-advanced" \
  "timing=0.1.0
leaderboard=0.1.0" \
  "timing-v0.1.0
timing-v0.1.1
leaderboard-v0.1.0" \
  "timing 0.1.1"

# Nothing new → no output.
run "nothing-new" \
  "timing=0.1.1
leaderboard=0.1.0" \
  "timing-v0.1.0
timing-v0.1.1
leaderboard-v0.1.0" \
  ""

# Empty marker (first deploy) → both at their newest.
run "first-deploy" \
  "" \
  "timing-v0.1.0
leaderboard-v0.2.0" \
  "timing 0.1.0
leaderboard 0.2.0"

# Newest is by semver, not lexical/creation order (0.10.0 > 0.2.0 > 0.1.0).
run "semver-newest" \
  "timing=0.1.0" \
  "timing-v0.1.0
timing-v0.2.0
timing-v0.10.0" \
  "timing 0.10.0"

# Malformed / foreign tags are ignored (no service emitted for them).
run "ignore-garbage" \
  "timing=0.1.0" \
  "v1.2.3
random-tag
timing-1.0.0
leaderboard-vfoo" \
  ""

if [ "$fails" -eq 0 ]; then echo "test-select-deploys: OK"; else echo "test-select-deploys: $fails FAILURE(S)" >&2; exit 1; fi
