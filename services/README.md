# services/

Each Pitwall service lives in its own self-contained directory here
(`services/<name>/` — own Dockerfile, datastore, tests, README, manifest). The **only** thing
shared between services is the wire contract in [`/contract`](../contract/); no service imports
another's code or reads its database.

**This directory is intentionally near-empty right now.** Pitwall is built
**walking-skeleton-first** — we *grow* the tree, we don't pre-scaffold it (architecture
§"Grow it, don't pre-scaffold it" / AR15). The first service code lands in **Story 1.3**
(the Go service skeleton on the bus); Timing + Leaderboard follow in the rest of Epic 1.

Planned layout (target — see `architecture.md` §"Target Directory Structure"):

```
services/
├── timing/        # Go  · + simulator
├── identity/      # Go  · master-UUID issuer
├── leaderboard/   # Go  · + embedded display bundle
├── control-room/  # Go  · dashboard + observation tap + privacy saga
├── driver/        # Python
├── crm/           # Python
├── billing/       # Python · PostgreSQL
├── mailing/       # Python · SMTP/Mailhog
├── frontend/      # TS · Next.js · credentials/auth · admin UI · payments edge
├── booking/       # TS · capacity authority
└── bar-pos/       # TS · front-of-house counter + simulator
```
