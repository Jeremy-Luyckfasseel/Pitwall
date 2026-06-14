# `libs/go-pitwall`

The shared Go **blueprint machinery** for Pitwall's Go services, extracted from the
Epic-1 walking skeleton (Timing + Leaderboard) once a second Go consumer existed
(architecture §Build Sequence step 2 — *grow, don't pre-scaffold*).

## Charter — mechanics ONLY

This library carries **blueprint mechanics only — never domain logic**. The moment a
service's exchange name, routing key, projection, or pricing rule leaks in, it becomes a
distributed monolith (architecture §Architectural Boundaries). Services pass their
topology (exchange names, routing keys, bindings, retry config, schema dir) *in*; the
library supplies the reusable plumbing:

- `logging` — single structured-JSON logger (timestamp · level · service · correlationId)
- `envelope` — the standard wire envelope codec
- `contract` — `/contract` JSON-Schema validator wrapper (validate-on-publish / -on-consume)
- `persistence` — SQLite open (WAL + busy-timeout + FK pragmas) · `WithinTx` · goose migrate runner · transactional **outbox** store · idempotent **inbox** (dedupe on envelope `id`)
- `messaging` — reconnect **supervisor** · publisher · consumer **Bus** · outbox **relay** · **DLQ / TTL-retry / parking** helpers
- `heartbeat` — 1 s liveness emitter (injected publisher + clock; unit-testable without a broker)
- `erasure` — reusable **erasure-handler scaffold + tombstone-guard** (inbox → service delete-slice callback → tombstone → ack `privacy.erased`), DG-3/DG-7

## Versioning & wiring

- **Semver'd** — see [CHANGELOG.md](CHANGELOG.md). Current: `v0.1.0`.
- **Build-time coupling, not runtime** — vendored into each service container, so it does
  not breach "share nothing but `/contract`".
- **Pinned per service** via a `replace` directive in each service `go.mod` (the
  Docker-reproducible pin) plus the repo-root `go.work` (local multi-module dev). A lib
  bump is therefore an **opt-in PR per service**, preserving independent per-service
  deploys (ADR-0007).
