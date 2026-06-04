# Contract Changelog

All notable changes to the Pitwall wire contract (`/contract`) are recorded here.

The contract is the **only** coupling between services, so every change is a cross-service event.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The contract is **not**
SemVer-tagged as a whole — each event payload carries its own integer `schemaVersion` (additive
changes do not bump it; breaking changes ship a new `vN` schema under a new routing key and run
side by side). See [`README.md`](README.md) for the normative wire rules.

## [Unreleased]

### Changed
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
