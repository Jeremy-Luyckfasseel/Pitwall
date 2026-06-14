#!/usr/bin/env sh
# Pure selection logic for the Pitwall deploy poller (Story 1.12 / ADR-0007 / Q29.1).
# Reads candidate tags on STDIN and a deployed-state marker file ("service=version" lines), and
# prints "<service> <version>" for each service whose NEWEST well-formed `‹svc›-vX.Y.Z` tag differs
# from what is recorded as deployed. Newest = highest semver (sort -V). Malformed/foreign tags are
# ignored. No git, no docker, no network — so it is unit-testable (deploy/test-select-deploys.sh).
set -eu

marker="${1:?usage: select-deploys.sh <deployed-marker-file>  (tags on stdin)}"
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
PARSE="$ROOT/scripts/parse-service-tag.sh"

# Collect valid "<service> <version>" pairs from the candidate tags.
pairs="$(
  while IFS= read -r tag; do
    [ -n "$tag" ] || continue
    out="$(sh "$PARSE" "$tag" 2>/dev/null)" || continue
    svc="$(printf '%s\n' "$out" | sed -n 's/^service=//p')"
    ver="$(printf '%s\n' "$out" | sed -n 's/^version=//p')"
    [ -n "$svc" ] && [ -n "$ver" ] && printf '%s %s\n' "$svc" "$ver"
  done
)"
[ -n "$pairs" ] || exit 0

# For each distinct service, pick the newest version and compare to the deployed marker.
for svc in $(printf '%s\n' "$pairs" | awk '{print $1}' | sort -u); do
  newest="$(printf '%s\n' "$pairs" | awk -v s="$svc" '$1==s{print $2}' | sort -V | tail -1)"
  deployed="$(grep -E "^${svc}=" "$marker" 2>/dev/null | head -1 | cut -d= -f2- || true)"
  [ "$newest" != "$deployed" ] && printf '%s %s\n' "$svc" "$newest"
done
exit 0
