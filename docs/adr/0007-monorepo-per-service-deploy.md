# ADR-0007 — Monorepo with per-service tag-driven deploys

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Q2.1, Q2.3–2.6 ·
**Branching amended Round 21 (2026-06-04): GitHub Flow, no long-lived `dev` branch (solo build).**

## Context
The brief wants `main`/`dev`/`prod` branches with auto-deploy. Services are folders in
one monorepo, not separate repos. The production VPS is small (7 GB RAM / 75 GB disk,
shared with another app), so a duplicate dev environment can't live there.

## Decision
- **Monorepo** with `services/<name>/` folders; each builds its own image.
- **Branching = GitHub Flow** *(amended Round 21)*: short-lived `story/<epic>.<story>-slug`
  branches → PR (squash) → `main`; **no long-lived `dev` branch** (solo build — story branches
  already isolate WIP and CI gates each PR). `main` is the always-green integration + release line.
  **Environments are unchanged** (the original `dev`-vs-prod split was about *where code runs*, not a
  git branch): dev/staging runs on the **local machine** via Compose; the **VPS hosts production
  only**. Deploy ≠ merge — merging to `main` never touches prod; only a per-service tag does.
- **Per-service tags** `‹svc›-vX.Y.Z` trigger CI to build that service's image, push to
  **GHCR**, and have the **VPS pull** + recreate **only that container**. Independent
  per-team versioning in a monorepo.
- **Orchestration**: Docker Compose now; Kubernetes a documented later stretch.
- **Config**: `.env` locally and on the VPS; `.env.example` committed; CI holds only
  GHCR + SSH creds.

## Consequences
- Each "team" releases independently despite the shared repo.
- Immutable images in GHCR give trivial rollback (redeploy previous tag).
- The VPS stays within budget by not doubling environments.
- A future memory/resource budget pass is deferred to deploy time.
