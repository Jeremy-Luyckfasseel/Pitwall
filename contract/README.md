# Pitwall Contract

The **only** thing shared between Pitwall's polyglot services. Every message on the bus
is JSON, wrapped in the common **envelope**, with its `data` payload validated against a
versioned **JSON Schema** here.

Full spec & event catalog: [`docs/analysis/02-message-bus-and-contracts.md`](../docs/analysis/02-message-bus-and-contracts.md).
Decision record: the wire-hardening rules below were ratified in
[Q&A Round 19](../docs/analysis/00-questions-and-answers.md#round-19--architecture-phase-master-uuid-register-first-polyglot-ai-assistant)
and `architecture.md` (Implementation Patterns).

## Layout
```
contract/
  README.md
  schemas/
    envelope.schema.json                     # the common envelope (every message)
    <service>/<entity>.<action>.v<N>.schema.json   # the data payload per event+version
  examples/
    <service>/<entity>.<action>.v<N>.example.json
```

## Wire Contract Rules (NORMATIVE — MUST / SHOULD / MAY per RFC-2119)

These are identical across Go / Python / TypeScript — the wire is canonical; internal code is
idiomatic per language and maps at the (de)serialization boundary. **Where this README and a schema
disagree, the schema wins; fix the README.**

- **Casing.** Every JSON field name — envelope and `data` — **MUST be `camelCase`**.
- **Envelope.** Every message **MUST** carry `id`, `type`, `source`, `schemaVersion`,
  `envelopeVersion`, `occurredAt`, `correlationId`, `data` (+ `causationId`, null for
  flow-originating events). `type` == the routing key.
- **Identifiers.** The canonical person id is **`masterId`** (issued solely by Identity) — a
  **lowercase canonical UUID v4** (`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).
  Envelope `id` is a lowercase canonical UUID, **v7 RECOMMENDED** (time-ordered). **Readers MUST
  normalize (lowercase) then validate**; brace/urn UUID forms are rejected.
- **Timestamps.** **MUST** serialize as exactly `YYYY-MM-DDTHH:MM:SS.mmmZ` — RFC3339 UTC, 3-digit
  milliseconds, literal `Z` (never `+00:00`).
- **Money.** **Integer minor units (cents)** + an ISO-4217 `currency` field. **No floats on the
  wire.** VAT / partial-spend division uses banker's rounding (round-half-even).
- **Big integers.** Any value that can exceed **2^53** (sequence/version counters, future epoch-ns)
  **MUST** be a decimal string (JS `number` is float64). Money in cents stays a JSON number (bounded).
- **Enums.** String fields with a fixed value set **MUST** declare a JSON Schema `enum` of exact
  lowercase values (e.g. `action: start|end`).
- **Events.** Routing key `<entity>.<action>`, lowercase; facts past-tense (`lap.recorded`), intents
  end `.requested` and are published to the **originating** service's own exchange.
- **Encoding.** UTF-8 (no BOM); AMQP `content-type: application/json`.

## Versioning & tolerant readers

- **Validate both sides.** Producers validate before publishing; consumers validate on receipt.
  Invalid → log + dead-letter, never silently drop.
- **Tolerant reader.** Schemas **MUST NOT** set `additionalProperties: false` on event objects, so an
  **additive** change (a new optional field) needs **no** `schemaVersion` bump; consumers ignore
  unknown fields. A **breaking** change → a new `vN` schema + new routing key/version, run side by
  side during migration.
- **Consume by vendoring / codegen.** Vendor (copy / submodule) into each service; wire DTOs **SHOULD**
  be code-generated from these schemas (`make contract`) so the three languages cannot drift.

## How each service uses it
1. Load `envelope.schema.json` + the schema for the event it's handling.
2. Validate on publish and on receive.
3. Contract tests in CI assert every produced/consumed message matches its schema — both a **valid
   example** and a **known-bad fixture** per event. (`scripts/validate-contract.py` runs the example
   pass; the corpus-coherence gate runs alongside it.)
