# Pitwall — Message Bus & Contracts

> The message contract is the **only** coupling point between services (they are
> polyglot and share nothing else). This document is the authoritative spec for the
> RabbitMQ topology, the message envelope, and the full event catalog. The machine-
> readable JSON Schemas live in [`/contract`](../../contract/). Traces to
> [`00-questions-and-answers.md`](./00-questions-and-answers.md) Rounds 1, 3, 5, 6, 10.

## 1. Topology — one topic exchange per owning service

- Each service **declares and owns exactly one durable `topic` exchange**, named
  `<service>.events` (e.g. `timing.events`, `driver.events`, `identity.events`).
- A service **publishes only to its own exchange.**
- A service **consumes** by binding its **own queue(s)** to the exchanges it cares
  about, using routing-key patterns.
- **Routing key** within an exchange: `<entity>.<action>` — e.g. in `timing.events`,
  the key `lap.recorded`. Fully-qualified, an event is identified by
  `<exchange>` + `<routing-key>`, e.g. `timing.events / lap.recorded`.
- All exchanges, queues, and messages are **durable / persistent**.

Rationale and trade-offs: [ADR-0001](../adr/0001-topic-exchange-per-service.md).

### Queue & binding conventions

- Each consuming service owns its queues, named `<consumer>.<purpose>` — e.g.
  `leaderboard.laps`, `billing.checkins`.
- Queues are **durable**, consumed with **manual acknowledgement** (ack only after the
  message is fully and successfully processed).
- Every consumer queue has a **dead-letter exchange** (`<consumer>.dlx`) and a
  dead-letter queue for poison messages, with **retry + exponential backoff** before a
  message lands in the DLQ.

## 2. Reliability model

| Mechanism | Purpose |
|---|---|
| Durable exchanges/queues, persistent messages | Survive broker restart |
| Manual ack after processing | No message lost on consumer crash mid-processing |
| Dead-letter queue + retry/backoff | Isolate poison messages without blocking the queue |
| **Outbox** (producer side) | State change + outgoing event committed atomically to the local DB; a relay publishes to RabbitMQ and retries until reachable → survives the bus being down |
| **Inbox / idempotency** (consumer side) | Each message id recorded; duplicates are no-ops → safe redelivery & replay |
| **Event store** | Every event persisted to a durable log so a restarting service can **replay** from its last-processed marker to catch up |

See [ADR-0002](../adr/0002-event-carried-state-transfer.md) and
[ADR-0005](../adr/0005-outbox-inbox-event-store.md).

## 3. The message envelope

Every message — regardless of producer language — is JSON with this envelope. The
domain payload lives under `data`; everything else is metadata. (Envelope-only fields
are inspired by CloudEvents but kept minimal.)

```jsonc
{
  "id": "0f4d…",              // UUID, unique per message — used by the inbox for dedupe
  "type": "lap.recorded",     // <entity>.<action>, matches the routing key
  "source": "timing",         // owning service name
  "schemaVersion": 1,         // integer; bumped only on breaking change (see §5)
  "occurredAt": "2026-05-31T14:03:21.512Z", // RFC3339 UTC, when the fact happened
  "correlationId": "8b2…",    // propagated across the whole causal chain (one driver's journey)
  "causationId": "1a9…",      // id of the message that directly caused this one (nullable)
  "data": { /* event-specific payload, validated against its JSON Schema */ }
}
```

Rules:
- `correlationId` is **created once** at the start of a flow (e.g. a gate scan or a
  Frontend intent) and **copied onto every downstream event** so a whole journey is
  traceable in the logs.
- Validation is **two-sided**: a producer validates before publishing, a consumer
  validates on receipt. A message failing validation is logged and dead-lettered —
  never silently dropped.

## 4. Event catalog

Illustrative and authoritative-by-intent; exact field lists are pinned by the JSON
Schemas in `/contract/schemas`. `→` lists primary consumers.

### `timing.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `driver.checked_in` | Driver scanned at the **entry gate** | `userId`, `sessionId?`, `at`, `gate` | Billing (open tab), Booking, Control Room |
| `lap.recorded` | Valid crossing at the **start-finish line** | `userId`, `sessionId`, `lapNumber`, `lapTimeMs`, `at` | Driver, Leaderboard, Control Room |
| `session.started` | **Actual** session start (physical) | `sessionId`, `startedAt` | Booking, Leaderboard, Billing |
| `session.ended` | **Actual** session end + summary | `sessionId`, `endedAt`, `summary[]` | Booking, Billing, Driver, Mailing, Leaderboard |
| `personal_record.broken` | A driver beat their all-time PR | `userId`, `sessionId`, `lapTimeMs`, `previousMs` | Driver, Mailing |
| `scanner.offline` | Scanner hardware went silent | `scannerId`, `since`, `gapFrom` | Control Room |
| `scanner.online` | Scanner recovered | `scannerId`, `at` | Control Room |

### `identity.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `identity.lookup_requested` | A service needs the UUID for an email | `requestId`, `email` | Identity |
| `identity.resolved` | Canonical UUID for that email | `requestId`, `email`, `userId` | the requester (Frontend, Timing/kiosk, …) |

### `driver.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `driver.profile_updated` | Racing profile changed | `userId`, racing fields | Frontend, Leaderboard, Timing |
| `driver.pr_updated` | Canonical all-time PR confirmed | `userId`, `lapTimeMs`, `setAt` | Timing (refresh local PR copy), Frontend |
| `driver.history_appended` | Session result stored | `userId`, `sessionId`, summary | Frontend |

### `crm.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `crm.person_updated` | Person/contact details changed | `userId`, name, email, phone, address | Billing, Mailing, Frontend |
| `crm.company_updated` | Company details changed | `companyId`, name, billing info | Billing |
| `crm.employee_linked` | Person linked to a company | `userId`, `companyId`, billTo | Billing |
| `crm.consent_changed` | Marketing/comms consent changed | `userId`, `marketingConsent` | Mailing |

### `booking.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `booking.requested` | (from Frontend) wants a slot | `requestId`, `userId`, `sessionId` | Booking |
| `booking.confirmed` | Slot reserved | `bookingId`, `userId`, `sessionId` | Frontend, Mailing, Billing |
| `booking.rejected` | Cannot reserve (e.g. full) | `requestId`, `reason`, `alternatives[]` | Frontend, Mailing |
| `session.scheduled` | Session added/updated in the plan | `sessionId`, plannedStart/end, capacity | Frontend, Leaderboard |
| `session.rescheduled` | Times shifted (cascade) | `sessionId`, newStart/end, `reason` | Frontend, Mailing, Leaderboard |
| `session.cancelled` | Session removed | `sessionId`, `reason` | Frontend, Mailing |

### `frontend.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `user.registration_submitted` | New signup (credentials stay in Frontend) | `requestId`, `email`, profile basics | Identity (then CRM/Driver) |
| `booking.requested` | see booking.events (published to frontend.events, bound by Booking) | | Booking |
| `bar.order_requested?` | (if ordered via site) | | Bar/POS |
| `profile.edit_requested` | Driver edits own profile | `userId`, `racing?`, `contact?`, `consent?` | Driver (racing), CRM (contact/consent) |
| `schedule.change_requested` | **Admin** schedule change (ADR-0010) | `action`, `sessionId?`, times/capacity, `adminActor` | Booking |
| `session.control_requested` | **Operator** start/end a session (ADR-0010) | `sessionId`, `action: start\|end`, `adminActor` | Timing |
| `privacy.erasure_requested` | Right-to-be-forgotten (ADR-0009) | `userId`, `requestedBy` | **all services** |
| `privacy.export_requested` | Data-portability request (ADR-0009) | `userId`, `requestedBy` | all owning services |

> Note: `*.requested` **intents** are published to the originating service's own
> exchange (Frontend owns `frontend.events`); the handling service binds to them. This
> keeps "publish only to your own exchange" intact.

### `billing.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `tab.opened` | Tab opened on check-in | `tabId`, `userId`, `sessionId` | Control Room |
| `tab.item_added` | Charge added (session/bar) | `tabId`, item, amount | — |
| `invoice.issued` | Receipt/invoice produced | `invoiceId`, `number`, `userId`/`companyId`, `documentRef`, `total` | Mailing |

### `mailing.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `email.sent` | Audit of a delivered email | `userId`, `template`, `at` | Control Room |
| `email.suppressed` | Marketing mail skipped (no consent) | `userId`, `template`, `reason` | Control Room |

### `bar.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `bar.order_placed` | Bar order to charge (`userId` **optional** — absent = anonymous sale, Round 15) | `orderId`, `userId?`, `items[]`, `total`, `at` | Billing |

### `control.events`
| Routing key | Meaning | Key payload | → Consumers |
|---|---|---|---|
| `heartbeat` | 1 s liveness from every service | `service`, `at`, `instanceId` | Control Room |
| `control.selfping` | Control Room's own 1 s bus probe | `at`, `nonce` | Control Room (itself) |
| `alert.raised` | Service/bus down etc. | `target`, `severity`, `detail`, `at` | Mailing, dashboard |
| `alert.cleared` | Recovery | `target`, `at` | dashboard |
| `privacy.export_ready` | Assembled data-export bundle ready (ADR-0009) | `userId`, `requestId`, `documentRef` | Mailing |

### `privacy.*` (cross-cutting — every service participates)

These two events are emitted by **each service to its own `<service>.events` exchange**
(the `source` envelope field identifies the emitter); the Control Room aggregates them.
The request side (`privacy.erasure_requested`, `privacy.export_requested`) lives in
`frontend.events`; the assembled-bundle side (`privacy.export_ready`) lives in
`control.events`. See [ADR-0009](../adr/0009-data-governance.md).

| Routing key | Owner exchange | Meaning | Key payload | → Consumers |
|---|---|---|---|---|
| `privacy.erased` | each `<service>.events` | Service erased/anonymized its slice for a `userId` | `userId`, `service`, `mode` | Control Room |
| `privacy.data_provided` | each `<service>.events` | Service's slice of an export bundle | `userId`, `service`, `payload` | Control Room |

## 5. Schema evolution policy

- Each event carries an integer `schemaVersion`.
- **Additive, backward-compatible** changes (new optional fields) **do not** bump the
  version. Consumers are **tolerant readers**: unknown fields are ignored.
- A **breaking** change (removing/renaming a field, changing a type/meaning) **bumps
  the version** and is published under a **new routing key/version**; old and new run
  side by side during migration until all consumers move.
- Schemas are versioned files in `/contract/schemas`; contract tests in CI fail the
  build if a service emits/consumes a message that doesn't match its schema.

See [ADR-0006](../adr/0006-json-schema-tolerant-reader.md).

## 6. The `/contract` folder

```
contract/
  README.md            # how to consume the contract; envelope spec; versioning rules
  schemas/
    envelope.schema.json
    timing/lap.recorded.v1.schema.json
    identity/identity.resolved.v1.schema.json
    ... (one file per event type + version)
  examples/
    timing/lap.recorded.v1.example.json
    ...
```

The contract lives in **this repo** (a `contract/` folder, not a separate repo). Each
service references it (vendored copy, submodule path, or generated client) and runs
its validation against these files.
