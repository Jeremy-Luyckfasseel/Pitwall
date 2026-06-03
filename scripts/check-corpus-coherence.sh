#!/usr/bin/env bash
# Corpus-coherence gate — keeps the docs/contract corpus from silently drifting from the
# decisions in docs/analysis/00-questions-and-answers.md (the golden rule). Ratified in Round 19.
set -uo pipefail

fail=0
# The Q&A log preserves history (Rounds 0-18 used "userId"); _bmad-output is planning scratch.
INCLUDES=(--include='*.md' --include='*.json' --include='*.go' --include='*.py' --include='*.ts' --include='*.tsx')
EXCLUDES=(--exclude-dir=_bmad-output --exclude-dir=.claude --exclude-dir=node_modules --exclude-dir=.git --exclude-dir=dist --exclude-dir=codegen)

# 1) The canonical person id is `masterId`, never `userId` as a field name (JSON/code: "userId").
#    Backtick-prose that *forbids* userId (in CLAUDE.md/CONTRIBUTING.md) is fine; only the quoted
#    field form signals real drift. The Q&A history log keeps its original wording.
hits=$(grep -RIn "${INCLUDES[@]}" "${EXCLUDES[@]}" '"userId"' . 2>/dev/null \
       | grep -v 'docs/analysis/00-questions-and-answers.md' || true)
if [ -n "$hits" ]; then
  echo "::error::Found \"userId\" as a field name — the canonical id is \"masterId\" (Q19.1). Offenders:"
  echo "$hits"
  fail=1
fi

# 2) Walk-ins are register-first; the old raw-token-buffer model is forbidden (Q6.4 / Q19.2).
hits=$(grep -RIn "${INCLUDES[@]}" "${EXCLUDES[@]}" \
        -e 'buffer the scan against the raw token' \
        -e 'buffered against the raw token' \
        -e 'held against the raw token' . 2>/dev/null || true)
if [ -n "$hits" ]; then
  echo "::error::Found raw-token-buffer language — walk-ins are register-first (Q6.4 / Round 19). Offenders:"
  echo "$hits"
  fail=1
fi

if [ "$fail" -eq 0 ]; then echo "corpus-coherence: OK"; fi
exit $fail
