# Pitwall — Service Blueprint (mandatory baseline)

> **Every** one of the 10 services + the Control Room must implement this baseline,
> regardless of language/framework/database. It is what makes a polyglot system behave
> as one coherent platform. A service is not "done" until every box applies.
> Traces to [`00-questions-and-answers.md`](./00-questions-and-answers.md) Round 11.

## The checklist

### Messaging
- [ ] Declares its own durable **`<service>.events` topic exchange**; publishes only to it.
- [ ] Owns its consumer queues (durable, manual ack), bound to the exchanges it needs.
- [ ] **Validates every message** (incoming and outgoing) against the `/contract` JSON
      Schemas. Invalid → log + dead-letter, never silently drop.
- [ ] Wraps every message in the standard **envelope** (`id`, `type`, `source`,
      `schemaVersion`, `occurredAt`, `correlationId`, `causationId`, `data`).
- [ ] **Tolerant reader**: ignores unknown fields; handles `schemaVersion`.

### Reliability
- [ ] **Outbox**: state change + outgoing event committed in one local transaction; a
      relay publishes to RabbitMQ and retries until reachable (survives bus-down). The relay
      **validates on publish**; an event that fails its own `/contract` validation is set to the
      terminal **`quarantined`** outbox status (`pending`/`sent`/`quarantined`) — never published,
      never retried. This **producer-side quarantine** is a local DB status, **not** the
      consumer-side RabbitMQ DLQ below; **every service uses this exact `quarantined` name**.
- [ ] **Idempotent inbox**: dedupes by message `id`; reprocessing is a no-op.
- [ ] **Dead-letter queue** (consumer side) + retry/backoff → parking for poison *inbound* messages.
- [ ] **Event-store/replay**: records its last-processed marker; on restart it catches
      up by replaying past that marker.
- [ ] Keeps its **own local copy** (read-model) of any peer data it needs (ECST) — no
      synchronous calls to other services.

### Liveness & health
- [ ] Publishes a **heartbeat every 1 second** to the Control Room over the bus.
- [ ] **No HTTP `/health` endpoint.** Docker `healthcheck:` uses a bus-connectivity
      script / liveness touch-file updated by the heartbeat loop.
- [ ] **Graceful shutdown**: finishes the in-flight message, flushes the outbox where
      possible, closes the channel/connection cleanly.

### Persistence & config
- [ ] Owns a **private database** (no shared DB, no cross-service queries).
- [ ] 12-factor **config via env vars**; documents each in the root `.env.example`.
- [ ] No secrets in code/logs/images.

### Observability
- [ ] **Structured JSON logs** with `service` + `correlationId` on every line.
- [ ] Propagates `correlationId` from the triggering event onto every event it emits.

### Data governance ([ADR-0009](../adr/0009-data-governance.md))
- [ ] Consumes `privacy.erasure_requested`; **deletes** its local slice for the `masterId`
      (Billing **anonymizes** under legal retention), writes a **tombstone** so a replayed/
      late event can't resurrect it, and emits `privacy.erased {masterId, service, mode}`.
- [ ] Consumes `privacy.export_requested`; emits `privacy.data_provided {masterId, service,
      payload}` with its slice of the subject's data.
- [ ] Stores only the **minimum PII** it needs (data minimization) and honors the retention
      window for the data it owns (financial 7 y · operational active+2 y · raw logs 90 d).

### Quality & delivery
- [ ] Unit + integration + contract tests; participates in the e2e smoke.
- [ ] Linter + formatter + pre-commit hooks; passes the CI gate.
- [ ] **Dockerfile**; builds an image pushed to GHCR by CI; deployed by per-service tag.
- [ ] **Sad-path table** in its service doc — every failure → a defined graceful
      outcome ("no computer says no").
- [ ] `README.md`: what it owns, events in/out, how to run, env vars.

## Reference shape of a service

```
services/<name>/
├── README.md
├── Dockerfile
├── healthcheck.(sh|js|py|…)      # bus-connectivity / liveness check (no HTTP)
├── <manifest>                    # package.json / pyproject.toml / go.mod / *.csproj …
├── src/
│   ├── messaging/                # exchange/queue setup, publisher (outbox), consumer (inbox)
│   ├── domain/                   # business logic (unit-testable, no I/O)
│   ├── persistence/              # private DB + outbox/inbox/event-store tables
│   ├── heartbeat/                # 1 s heartbeat publisher + liveness touch-file
│   └── config/                   # env loading + validation
└── test/
    ├── unit/  ├── integration/  └── contract/
```

## Cross-cutting building blocks (implement once per language, reuse)

Because services are polyglot, these can't be a single shared library — but each
language should have **one** internal implementation reused across its services:

- **Envelope + schema validator** (validate against `/contract`).
- **Outbox publisher** (transactional enqueue + background relay with retry).
- **Idempotent inbox consumer** (dedupe + manual ack + DLQ wiring).
- **Heartbeat emitter** (1 s, + liveness touch-file).
- **Structured logger** (JSON, correlation-id aware).
