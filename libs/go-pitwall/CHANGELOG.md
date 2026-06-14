# Changelog — `libs/go-pitwall`

All notable changes to the shared Go blueprint library. Semver; this library is
replace-pinned per service, so a bump is an opt-in PR per consumer.

## v0.1.0 — 2026-06-14

Initial extraction from the Epic-1 walking skeleton (Story 2.1). Blueprint mechanics
only, no domain logic.

- `logging` — structured-JSON logger.
- `envelope` — standard wire-envelope codec.
- `contract` — `/contract` JSON-Schema validator wrapper (contractDir-parameterized).
- `persistence` — SQLite open/pragmas, `WithinTx`, goose migrate runner, transactional
  outbox store, idempotent inbox (dedupe on envelope `id`).
- `messaging` — reconnect supervisor, publisher, consumer Bus, outbox relay,
  DLQ / TTL-retry / parking helpers.
- `heartbeat` — 1 s liveness emitter (injected publisher + clock).
- `erasure` — reusable erasure-handler scaffold + tombstone-guard helper (DG-3/DG-7).
