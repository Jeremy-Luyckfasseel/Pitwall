#!/usr/bin/env sh
# Test-first spec for scripts/parse-service-tag.sh (Story 1.12, Task 2).
# The parser turns a per-service release tag `‹svc›-vX.Y.Z` into `service=` / `version=`
# lines on stdout (exit 0), and REJECTS anything that is not a well-formed per-service tag
# (exit non-zero) so a stray tag can never trigger a build of the wrong/empty service.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
PARSE="$ROOT/scripts/parse-service-tag.sh"
fails=0

# expect_ok <tag> <service> <version>
expect_ok() {
  out="$(sh "$PARSE" "$1" 2>/dev/null)" || { echo "FAIL: '$1' should be VALID but exited non-zero"; fails=$((fails+1)); return; }
  if [ "$out" != "service=$2
version=$3" ]; then
    echo "FAIL: '$1' → expected [service=$2 version=$3], got [$(echo "$out" | tr '\n' ' ')]"; fails=$((fails+1))
  fi
}
# expect_bad <tag>
expect_bad() {
  if sh "$PARSE" "$1" >/dev/null 2>&1; then
    echo "FAIL: '$1' should be REJECTED but exited 0"; fails=$((fails+1))
  fi
}

expect_ok "timing-v0.1.0"        "timing"      "0.1.0"
expect_ok "leaderboard-v2.3.4"   "leaderboard" "2.3.4"
expect_ok "timing-v1.0.0-rc.1"   "timing"      "1.0.0-rc.1"   # prerelease suffix allowed
expect_ok "timing-v10.20.30"     "timing"      "10.20.30"     # multi-digit

expect_bad "v1.2.3"          # no service segment
expect_bad "foo-v1"          # not a full X.Y.Z
expect_bad "timing-1.0.0"    # missing the 'v'
expect_bad "Timing-v1.0.0"   # uppercase service (GHCR names are lowercase)
expect_bad "timing-v1.0"     # X.Y only
expect_bad ""                # empty
expect_bad "timing-vfoo"     # non-numeric version

if [ "$fails" -eq 0 ]; then
  echo "test-parse-service-tag: OK"
else
  echo "test-parse-service-tag: $fails FAILURE(S)" >&2; exit 1
fi
