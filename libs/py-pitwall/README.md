# `libs/py-pitwall`

The shared Python **blueprint machinery** for Pitwall's Python services — the
Python counterpart of `libs/go-pitwall`, standing up alongside `make contract`
codegen with the arrival of the second language (Driver, Story 3.1 —
architecture §Build Sequence step 4).

## Charter — mechanics ONLY

This library carries **blueprint mechanics only — never domain logic**. The moment a
service's exchange name, routing key, projection, or pricing rule leaks in, it becomes a
distributed monolith (architecture §Architectural Boundaries). Services pass their
topology (exchange names, routing keys, bindings, retry config, contract dir) *in*; the
library supplies the reusable plumbing:

- `pitwall.logging` — single structured-JSON logger (timestamp · level · service · correlationId)
- `pitwall.envelope` — the standard wire envelope codec, built on the `pitwall_contract`
  generated Pydantic model (`contract/codegen/python`)
- `pitwall.validate` — `/contract` JSON-Schema validator wrapper (`jsonschema`-backed;
  validate-on-publish / -on-consume; deliberately independent of the generated Pydantic
  models — codegen and validation are separate concerns, same as the Go library)
- `pitwall.persistence` — SQLite open (WAL + busy-timeout + FK pragmas) · Alembic
  migration runner · transactional **outbox** store · idempotent **inbox** (dedupe on
  envelope `id`)
- `pitwall.messaging` — `pika`-based (sync, Q&A Round 34/Q34.2) reconnect supervisor ·
  publisher · consumer · outbox relay · **DLQ / TTL-retry / parking** helpers
- `pitwall.heartbeat` — 1 s liveness emitter (injected publisher + clock;
  unit-testable without a broker)
- `pitwall.erasure` — reusable **erasure-handler scaffold + tombstone-guard** (inbox →
  service delete-slice callback → tombstone → ack `privacy.erased`), DG-3/DG-7

Note: no FastAPI anywhere in this library or any Python service it backs — Pitwall
services are bus-only (CLAUDE.md §2 rules 1–2); FastAPI's Pydantic v2 is used directly
without the ASGI runtime (Q&A Round 34 / Q34.1).

## Versioning & wiring

- **Semver'd** — see [CHANGELOG.md](CHANGELOG.md). Current: `v0.1.0`.
- **Build-time coupling, not runtime** — vendored into each service container, so it
  does not breach "share nothing but `/contract`".
- **Pinned per service** via two explicit `pip install -e` steps (the Python analogue
  of Go's `replace` directive): first `pip install -e contract/codegen/python` (the
  generated wire DTOs this library maps to/from), then `pip install -e
  libs/py-pitwall`. A lib bump is therefore an **opt-in step per service**, preserving
  independent per-service deploys (ADR-0007).
