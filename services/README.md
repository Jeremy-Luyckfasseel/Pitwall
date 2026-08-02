# services/

Each Pitwall service lives in its own self-contained directory here
(`services/<name>/` — own Dockerfile, datastore, tests, README, manifest). The **only** thing
shared between services is the wire contract in [`/contract`](../contract/); no service imports
another's code or reads its database.

**We grow the tree, we don't pre-scaffold it** (architecture §"Grow it, don't pre-scaffold it"
/ AR15). The first service code landed in **Story 1.3**: [`timing/`](timing/) hosts the **Go
service skeleton on the bus** (heartbeat · structured logs · graceful shutdown). **Story 1.7**
added [`leaderboard/`](leaderboard/) — the first **consumer**: it consumes Timing's
`lap.recorded`, dedupes via an idempotent inbox, folds best-lap standings, and serves a live
trackside board over SSE. **Story 1.8** made it **session-aware**: it consumes
`session.started`/`session.ended`, auto-resets per session (session-keyed standings + a local
epoch), shows active/finished status, and tolerates out-of-order/replayed events (NFR24). The
Go blueprint machinery built inline in `timing/` (and duplicated in `leaderboard/`) was
extracted to `libs/go-pitwall` in **Epic 2**, alongside [`identity/`](identity/) — the
canonical-`masterId` issuer every other service joins on.

**Epic 3** brought the platform's **second language**: [`driver/`](driver/) is the first
**Python** service (Story 3.1), built on `libs/py-pitwall` (the Python counterpart of
`libs/go-pitwall`) and the generated wire DTOs in `contract/codegen/python/`. It ships as a
skeleton — bus connection, heartbeat, structured logs, graceful shutdown — with racing-profile/
lap-history domain logic landing in Stories 3.2+.

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
