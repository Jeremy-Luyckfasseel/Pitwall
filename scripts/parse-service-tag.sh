#!/usr/bin/env sh
# Parse a per-service release tag `‹svc›-vX.Y.Z[-prerelease]` (Story 1.12 / ADR-0007).
# Usage: parse-service-tag.sh <tag>
#   prints two lines on success (exit 0):   service=<svc>\nversion=<X.Y.Z[-pre]>
#   prints an error and exits non-zero on any tag that is NOT a well-formed per-service tag.
# Shared by release.yml (which service to build + image tag) and the VPS poller (selection).
# Rules: service is lowercase dns-ish (GHCR names are lowercase); version is full semver
# X.Y.Z with an optional dotted/hyphenated prerelease suffix; the literal 'v' is required.
set -eu

tag="${1-}"
[ -n "$tag" ] || { echo "parse-service-tag: empty tag" >&2; exit 1; }

# service = everything before the last "-v<digit…>"; version = the rest after "-v".
service="${tag%-v[0-9]*}"
rest="${tag#"$service"-v}"   # strip "<service>-v" → leaves the version

# Reject if the split didn't actually find a "-v<digit>" boundary.
[ "$service" != "$tag" ] || { echo "parse-service-tag: '$tag' is not '<svc>-vX.Y.Z'" >&2; exit 1; }

# Validate service: lowercase letter, then lowercase alnum/hyphen.
echo "$service" | grep -Eq '^[a-z][a-z0-9-]*$' \
  || { echo "parse-service-tag: invalid service segment '$service' (lowercase dns-ish required)" >&2; exit 1; }

# Validate version: X.Y.Z with an optional prerelease (-alnum/dot/hyphen).
echo "$rest" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' \
  || { echo "parse-service-tag: invalid version '$rest' (need X.Y.Z[-prerelease])" >&2; exit 1; }

printf 'service=%s\nversion=%s\n' "$service" "$rest"
