# Contributing to Pitwall

Pitwall is a polyglot, event-driven, bus-only platform (10 services + Control Room on RabbitMQ).
Start with **[`CLAUDE.md`](CLAUDE.md)** (the operating contract — the golden rule: *never assume; every
decision is recorded in [`docs/analysis/00-questions-and-answers.md`](docs/analysis/00-questions-and-answers.md)*)
and the full design in [`docs/analysis/`](docs/analysis/).

## Golden rule

If a requirement isn't recorded in the Q&A log, it's an **open question** — **stop and ask**, then
record the answer there **before** building. Decisions land as a Q&A row (+ an ADR if they change
architecture) in the **same commit** as the change — never "queued for later".

## The wire is the only coupling

Services share **nothing** but the message contract in [`/contract`](contract/). The wire is canonical
across Go / Python / TypeScript; internal code stays idiomatic per language and maps at the
(de)serialization boundary.

**Wire Contract Rules (digest — full normative spec in [`contract/README.md`](contract/README.md)):**

- **MUST:** camelCase field names; the standard envelope (`id`, `type`, `source`, `schemaVersion`,
  `envelopeVersion`, `occurredAt`, `correlationId`, `causationId`, `data`); canonical person id
  **`masterId`** (lowercase UUID v4); envelope `id` lowercase UUID (v7 recommended); timestamps
  `YYYY-MM-DDTHH:MM:SS.mmmZ`; money as **integer minor units (cents)** + ISO-4217 `currency`; enum
  string fields pinned via JSON Schema `enum`; routing key `<entity>.<action>` (facts past-tense,
  intents `.requested`, published to your own `<service>.events` exchange).
- **MUST NOT:** `additionalProperties: false` on event objects (breaks tolerant-reader / additive
  evolution); `userId` (renamed to `masterId`); the raw-token-buffer walk-in model (walk-ins are
  **register-first** — everyone resolves a `masterId` at check-in before racing).
- **Validate both sides** (publish + consume) against `/contract`; invalid → log + dead-letter, never
  silently drop. Wire DTOs should be **code-generated** from the schemas (`make contract`).

## Per-service baseline

Every service conforms to the [service blueprint](docs/analysis/04-service-blueprint.md): outbox +
idempotent inbox + event-store/replay + DLQ (TTL-retry → delivery-count-capped parking); 1 s heartbeat
(no HTTP `/health`); structured JSON logs with a propagated `correlationId`; its own private database;
a sad-path table; four test layers (unit + integration on real RabbitMQ+DB + contract + e2e smoke).

## Workflow

- **Branches = environments** (`dev` local; `prod`/`main` release line, VPS = prod only). **Per-service
  tags** `‹svc›-vX.Y.Z` build → GHCR → VPS pulls only the changed container.
- **Conventional Commits**; per-language linter + formatter + pre-commit hooks; **no merge on red**.
- **Definition of Done:** see [`CLAUDE.md` §5](CLAUDE.md).

### Story tracking (commit ↔ story linkage)

Story status lives in the BMAD sprint tracker (`_bmad-output/`, local). The durable **commit ↔ story**
link lives in git itself — no external ticketing system:

- **Every commit toward a story carries a `Story:` trailer** (epic.story id from
  [`epics.md`](docs/analysis/) — e.g. the walking skeleton is Epic 1):

  ```
  feat(timing): add minimum-lap-time bounce filter

  Story: 1.6
  ```

- **Branch per story:** `story/<epic>.<story>-<slug>` (e.g. `story/1.6-lap-validity`).
- **Trace a story's history anytime:** `git log --grep "Story: 1.6"` (all its commits), or
  `git log --grep "Story: 1\."` for the whole epic. The git history *is* the source of truth — it never
  drifts from a board.
- If GitHub Issues are later adopted, add `Refs #<n>` alongside the `Story:` trailer; until then the
  trailer alone is sufficient.

## Local checks

```bash
bash scripts/check-corpus-coherence.sh   # corpus stays coherent (no userId / no raw-token-buffer)
python scripts/validate-contract.py      # every /contract example validates (pip install jsonschema)
```

Both run in CI (`.github/workflows/corpus-coherence.yml`).
