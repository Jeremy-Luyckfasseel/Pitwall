# ADR-0007 — Monorepo with per-service tag-driven deploys

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Q2.1, Q2.3–2.6

## Context
The brief wants `main`/`dev`/`prod` branches with auto-deploy. Services are folders in
one monorepo, not separate repos. The production VPS is small (7 GB RAM / 75 GB disk,
shared with another app), so a duplicate dev environment can't live there.

## Decision
- **Monorepo** with `services/<name>/` folders; each builds its own image.
- **Branches = environments**: `dev` = integration (run **locally** via Compose),
  `prod`/`main` = release line. **Dev/staging runs on the local machine; the VPS hosts
  production only.**
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
