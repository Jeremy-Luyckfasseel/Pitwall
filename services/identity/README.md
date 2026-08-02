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
| **Consumes** | `privacy.erasure_requested` | `frontend.events` | bound on the SAME queue as the lookup (one queue carries both intents; they share the same producer exchange) |
| **Publishes** | `identity.resolved` | `identity.events` (its **own**) | sent durably via the outbox + confirm-relay |
| **Publishes** | `privacy.erased` | `identity.events` (its **own**) | sent durably via the SAME outbox + confirm-relay (Story 2.6) |
| **Publishes** | `control.heartbeat` | `identity.events` | 1 s liveness (ADR-0004) |

Identity is the first service that is **both a consumer and an outbox-backed producer**:
a consumer Bus binds `frontend.events` (and publishes the heartbeat), while a confirm
publisher drives the outbox relay that publishes `identity.resolved` and `privacy.erased`.
Both declare `identity.events` idempotently.

## Erasure & the email-hash tombstone (Story 2.6, DG-3/DG-7)

Identity is the first service to wire [`libs/go-pitwall/erasure`](../../libs/go-pitwall/erasure)'s
reusable inbox-dedupe → delete-slice → tombstone → atomic ack pipeline into a real domain.
A validated `privacy.erasure_requested {masterId}` deletes the `identities` row for that
`masterId`, records a durable tombstone (`identity_tombstones`), and durably enqueues
`privacy.erased {requestId, masterId, service:"identity", mode:"deleted", at}` to the
SAME outbox `identity.resolved` uses — all atomically, so the ack can never be silently
lost.

Because Identity's own slice **is** the email↔`masterId` mapping, a full delete alone
would let a later lookup for the same address silently mint a brand-new identity —
undoing the erasure (Round 33/Q33.1). So the erased email's normalized form is hashed
(irreversible SHA-256, never the plaintext) into a small suppression table
(`email_suppressions`) before the row is deleted. A later `identity.lookup_requested`
for a suppressed email is **held**: durably persisted (`held_lookups`, never dropped) and
logged at alert severity, but never minted and never replied to — the exact "hold +
persist + flag" shape Story 2.5 established for its own unknown-token exception.

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
| **Erasure request for a live `masterId`** | Delete + tombstone + `privacy.erased` ack, all atomic. |
| **Lookup for an email suppressed by a prior erasure** | Held + persisted (`held_lookups`) + logged at alert severity; never minted, never replied. |
| **A second erasure request for an already-erased `masterId`** (different envelope id) | Graceful no-op delete (idempotent), a harmless duplicate `privacy.erased` ack. |

## Layout

- `internal/persistence/erasure_store.go` — `ErasureStore`: delete-slice + email-hash
  suppression + tombstone, all tx-scoped (Story 2.6).
- `internal/persistence/held_lookup_store.go` — `HeldLookupStore`: the durable "held,
  never dropped" sink for a suppressed-email lookup.
- `internal/persistence/migrations/0002_erasure.sql` — `identity_tombstones`,
  `email_suppressions`, `held_lookups`.
- `internal/domain/erasure.go` — `HashEmail`: pure SHA-256 hash of an already-normalized
  email.

## Run / test

```sh
cd services/identity
GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...
GOWORK=off go test -tags=integration -timeout 600s ./test/integration/...   # real RabbitMQ via testcontainers (needs Docker)
```

The whole stack (broker + timing + leaderboard + identity) runs via `docker compose up`
from the repo root. Liveness is **bus-only** — a 1 s heartbeat + a touch-file Docker
healthcheck, never an HTTP `/health` (ADR-0004). No published ports.
