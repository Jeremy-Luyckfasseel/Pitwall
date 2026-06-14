#!/usr/bin/env sh
# Release-workflow invariants gate (Story 1.12, Task 2). Asserts .github/workflows/release.yml
# realizes ADR-0007 + Round-29 structurally — a grep gate (actionlint is not vendored here):
#   1. it triggers on TAGS only, never on a branch push → AC3 "deploy is tag-driven, not
#      branch-driven" (merging to main must NOT deploy);
#   2. it parses the tag via scripts/parse-service-tag.sh (one service per tag);
#   3. it builds with context = repo ROOT (the Dockerfiles need /contract) against
#      services/<svc>/Dockerfile;
#   4. it pushes a PUBLIC GHCR image ghcr.io/<owner>/pitwall-<svc> (push: true);
#   5. it grants packages: write and uses NO SSH secret (poller deploy is pull-based — Q29.1).
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
WF="$ROOT/.github/workflows/release.yml"
fail() { echo "check-release-workflow: FAIL — $1" >&2; exit 1; }

[ -f "$WF" ] || fail "missing .github/workflows/release.yml"

# 1. Tag-only trigger; never branch-driven (AC3).
grep -Eq '^[[:space:]]*tags:' "$WF" || fail "release.yml must trigger 'on: push: tags:'"
grep -Eq '^[[:space:]]*branches:' "$WF" && fail "release.yml must NOT trigger on branches (deploy ≠ merge — AC3)"

# 2. Uses the shared per-service tag parser.
grep -q 'parse-service-tag.sh' "$WF" || fail "release.yml must parse the tag via scripts/parse-service-tag.sh"

# 3. Build context = repo root, per-service Dockerfile.
grep -Eq '^[[:space:]]*context:[[:space:]]*\.([[:space:]#].*)?$' "$WF" || fail "build context must be the repo root ('context: .')"
grep -q 'services/.*/Dockerfile' "$WF" || fail "release.yml must build services/<svc>/Dockerfile"

# 4. Pushes a public GHCR pitwall image.
grep -q 'ghcr.io/' "$WF" || fail "release.yml must push to ghcr.io"
grep -q 'pitwall-' "$WF" || fail "image name must be pitwall-<svc>"
grep -Eq '^[[:space:]]*push:[[:space:]]*true' "$WF" || fail "build-push must actually push (push: true)"

# 5. packages: write, and NO ssh/deploy-key secret (pull-based poller — Q29.1).
# Strip '#' comments first so the explanatory prose ("no SSH secret") is not mistaken for real usage;
# then flag only genuine SSH-action / SSH-secret patterns.
grep -Eq '^[[:space:]]*packages:[[:space:]]*write' "$WF" || fail "release.yml needs 'permissions: packages: write'"
if sed 's/#.*$//' "$WF" | grep -Eiq 'ssh-action|ssh-agent|known_hosts|secrets\.[A-Za-z_]*(SSH|DEPLOY_KEY)|appleboy'; then
  fail "release.yml must hold NO SSH/deploy-key secret — the VPS poller pulls (Q29.1)"
fi

echo "check-release-workflow: OK — tag-only, parser-driven, root context, public GHCR push, no SSH secret."
