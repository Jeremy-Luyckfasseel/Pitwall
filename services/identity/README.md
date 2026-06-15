# Identity service (Go)

The sole issuer of the canonical **`masterId`** — exactly one per person, de-duplicated
on **email** (FR1/FR2/FR3, NFR15, [ADR-0003](../../docs/adr/0003-identity-as-uuid-issuer.md)).
Deliberately minimal: Identity stores **only** `masterId` + email + status + timestamps —
no passwords, profile, racing data, or fuzzy merging.

Built on [`libs/go-pitwall`](../../libs/go-pitwall) (Story 2.1): all blueprint mechanics
(envelope codec, `/contract` validator, transactional outbox + relay, idempotent inbox,
consumer Bus + DLQ/retry/parking, reconnect supervisor, heartbeat, structured logger,
graceful shutdown) are reused. Identity adds only its domain (resolve-or-mint), its
topology, and its migrations.

## The one operation: resolve-or-mint

A register-first flow needs a `masterId` for an email, so it publishes
`identity.lookup_requested {requestId, email}`. Identity:

1. **Known email** → returns the existing `masterId`.
2. **Unknown email** → mints a new lowercase **UUID v4**, persists it (email is the
   unique natural key), returns it.
3. Replies `identity.resolved {requestId, email, masterId}`.

There is **no `isNew` flag** — consumers idempotently upsert on the returned `masterId`
(FR2). Everything is event-driven over RabbitMQ (no synchronous API; bus-only rule holds).

## Events

| Direction | Routing key | Exchange (physical) | Notes |
|---|---|---|---|
| **Consumes** | `identity.lookup_requested` | `frontend.events` (the **originating** service's exchange — Q&A Round 30; later also `bar.events` for counter walk-ins, Epic 7) | bound via a durable queue `identity.lookup-requested` |
| **Publishes** | `identity.resolved` | `identity.events` (its **own**) | sent durably via the outbox + confirm-relay |
| **Publishes** | `control.heartbeat` | `identity.events` | 1 s liveness (ADR-0004) |

Identity is the first service that is **both a consumer and an outbox-backed producer**:
a consumer Bus binds `frontend.events` (and publishes the heartbeat), while a confirm
publisher drives the outbox relay that publishes `identity.resolved`. Both declare
`identity.events` idempotently.

## Idempotency (two independent guarantees)

- **Inbox dedupe on the envelope `id`** — an at-least-once **redelivery** of the same
  `identity.lookup_requested` is a safe no-op (no second mint, no second reply).
- **`UNIQUE(email)` single-writer** — two **distinct** lookups for the same *new* email
  resolve to exactly one `masterId` (the conflicting INSERT is a no-op; the loser
  re-reads the winner's id).

The resolve-or-mint + the `identity.resolved` outbox-enqueue + the inbox-mark commit in
**one transaction** before the ack (consumer-side ECST).

## Sad-path table

| Scenario | Handled outcome |
|---|---|
| Two lookups for the same **new** email race | `UNIQUE(email)`: one mint, the other reuses. Exactly one `masterId`. |
| Lookup for an **existing** email | Returns the existing `masterId` — no duplicate. |
| **Redelivered** same envelope id | Inbox dedupe → ack no-op; no second reply/mint. |
| **RabbitMQ down** | Lookups redeliver on reconnect; the reply is committed to the outbox and the relay publishes it on reconnect (retry-forever + supervisor). |
| **Malformed lookup** (bad/missing email or requestId) | Validate-on-consume fails → log + **park** (DLQ); no `identity.resolved` emitted, never silently dropped. |
| **Service restart** | Durable store + inbox + outbox; replays unprocessed lookups; idempotent. |

## Run / test

```sh
cd services/identity
GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...
GOWORK=off go test -tags=integration -timeout 600s ./test/integration/...   # real RabbitMQ via testcontainers (needs Docker)
```

The whole stack (broker + timing + leaderboard + identity) runs via `docker compose up`
from the repo root. Liveness is **bus-only** — a 1 s heartbeat + a touch-file Docker
healthcheck, never an HTTP `/health` (ADR-0004). No published ports.
