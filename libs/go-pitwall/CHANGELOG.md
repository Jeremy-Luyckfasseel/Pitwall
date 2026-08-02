# Changelog — `libs/go-pitwall`

All notable changes to the shared Go blueprint library. Semver; this library is
replace-pinned per service, so a bump is an opt-in PR per consumer.

## v0.2.0 — 2026-08-01

Story 2.6 (Identity erasure handler and tombstone): closed a real outbox-atomicity gap
in the erasure scaffold ahead of its first production caller.

- `erasure.Handler` gains a required `Enqueue` field (`AckEnqueuer`): the `privacy.erased`
  ack is now enqueued INSIDE the same transaction as the delete-slice + tombstone write,
  matching the atomic-outbox-enqueue convention every other producer in this codebase
  already follows (`librelay.EnqueueEnvelope` called inside the producing tx). Previously
  `Handle` returned the ack for the caller to enqueue in a SEPARATE transaction — a crash
  between the two could permanently lose the ack (the inbox was already marked seen).
  **Breaking:** `Handler.Enqueue` is required; existing callers must supply it.

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
