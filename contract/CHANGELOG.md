# Contract Changelog

All notable changes to the Pitwall wire contract (`/contract`) are recorded here.

The contract is the **only** coupling between services, so every change is a cross-service event.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The contract is **not**
SemVer-tagged as a whole — each event payload carries its own integer `schemaVersion` (additive
changes do not bump it; breaking changes ship a new `vN` schema under a new routing key and run
side by side). See [`README.md`](README.md) for the normative wire rules.

## [Unreleased]

### Added
- **`timing/driver.checked_in.v1`** data-payload schema + valid example + known-bad fixture — the
  entry-gate check-in fact (`masterId`, `at`, `checkInMethod` ∈ {`qr`,`transponder`}, nullable
  `transponderId`). Published to `timing.events` (routing key `driver.checked_in`). QR carries the
  `masterId` directly (no lookup at the gate); a transponder hardware id is resolved via Timing's local
  map. `masterId`/`at` reuse the strict `lap.recorded` patterns; `transponderId` is `["string","null"]`
  and always present (null for QR drivers, AR9). The known-bad fixture breaks the `checkInMethod` enum
  (`"badge"`). *(Story 2.3)*
- **`control/control.heartbeat.v1`** data-payload schema + valid example + known-bad fixture — the 1 s
  bus-only liveness signal emitted by **every** service (payload `service`, `at`, `instanceId`; `at`
  carries the canonical timestamp `pattern`). Like `privacy.erased`, it is **cross-cutting**: each
  service publishes it to its **own** `<service>.events` exchange (`source` names the emitter, routing
  key `control.heartbeat`); the `control` namespace is the catalog grouping, not the owning exchange.
  The known-bad fixture breaks the `at` wire-format (uses a `+00:00` offset instead of `…mmmZ`).
  Routing key is `control.heartbeat` (not a bare `heartbeat`) because the envelope `type` requires the
  `<entity>.<action>` form (Q&A Round 25). *(Story 1.3)*
- **`timing/session.started.v1`** and **`timing/session.ended.v1`** data-payload schemas + valid
  examples — the two remaining walking-skeleton events (`session.started`: `sessionId`, `startedAt`;
  `session.ended`: `sessionId`, `endedAt`, `summary[]`). `summary[]` item shape is intentionally
  left tolerant for v1 (no Epic-1 consumer reads it; pin when Driver/Mailing do). *(Story 1.2)*
- **Known-bad fixtures** (`*.invalid.json`) for all three `timing` events
  (`lap.recorded`, `session.started`, `session.ended`), each documenting its breakage via an
  `_invalidReason` key — collectively covering uppercase UUID, float-for-integer,
  snake_case-replacing-required, and missing-required. *(Story 1.2)*
- **Negative contract gate** `scripts/check-invalid-fixtures.py` (+ `--selftest`, wired into CI and
  `make contract-test`): asserts every `*.invalid.json` is rejected and enforces example↔invalid
  pairing for the `timing` namespace, so a deleted or no-longer-bad fixture fails the build. *(Story 1.2)*

### Changed
- **`timing/lap.recorded.v1`:** `masterId` now pins the canonical **UUID-v4 `pattern`**
  (`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`) in addition to
  `format: uuid`. JSON-Schema `format` is annotation-only and is **not** asserted by the validator,
  so the pattern is what actually rejects a malformed `masterId` (AR8). No valid payload changed. *(Story 1.2)*
- **Timing timestamp fields pinned:** `lap.recorded.at`, `session.started.startedAt`, and
  `session.ended.endedAt` now carry the canonical timestamp `pattern`
  (`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`) in addition to `format: date-time`, matching the
  envelope's `occurredAt`. `format` alone is annotation-only; the pattern enforces the exactly-3-digit-
  millis-and-literal-`Z` wire rule (AR9). All committed examples already conform; no valid payload
  changed. *(Story 1.2)*
- **Envelope:** `causationId` is now a **required** field (was effectively optional). It remains
  typed `["string", "null"]`, so flow-originating events still validate by sending
  `causationId: null` — the key must be **present**, never omitted. This aligns the schema with
  the "envelope — all fields present" rule (architecture AR7) and Story 1.1 AC3. All existing
  examples already carried the key, so no payloads changed. *(Story 1.1)*

## [Skeleton] — 2026-06-04

Initial contract skeleton committed ahead of the walking skeleton (Epic 1).

### Added
- `envelope.schema.json` — the common message envelope: `id`, `type`, `source`, `schemaVersion`,
  `envelopeVersion`, `occurredAt`, `correlationId`, `causationId`, `data`. Tolerant-reader
  (`additionalProperties: true`); pins `id`/`correlationId`/`causationId` to lowercase-UUID,
  `occurredAt` to `YYYY-MM-DDTHH:MM:SS.mmmZ`, camelCase field names.
- Event schemas + valid example fixtures (`schemas/` + `examples/`) across the `timing`,
  `identity`, `frontend`, `bar`, `billing`, `control`, and `privacy` namespaces, seeded during the
  PRD/architecture back-record (Rounds 13–22). These predate the Epic 1 build; they are validated
  in CI by `scripts/validate-contract.py`.

[Unreleased]: https://github.com/Pitwall/compare/skeleton...HEAD
