# Service: Driver

> Source of truth for the **racing identity**: racing profile, full lap history, and
> the canonical personal record. Decisions: [Q&A](../00-questions-and-answers.md)
> Round 7. Conforms to the [service blueprint](../04-service-blueprint.md).

## Purpose
Store who a driver is *as a racer* and their complete performance history across all
sessions, keyed on the canonical `userId`.

## System of record (owns)
- **Racing profile**: racing number, preferred kart class, leaderboard nickname,
  racing stats. (Human/contact data is CRM's; both share the `userId`.)
- **Full lap-by-lap history** across every session.
- **Per-session summaries**.
- **Canonical all-time PR** per driver.

## Compute vs store (PR)
Timing *detects* a broken PR from the live lap stream; **Driver is the system of
record**. On `personal_record.broken` (and from the lap history), Driver confirms and
stores the canonical PR, then publishes `driver.pr_updated` so Timing refreshes its
local PR copy and Frontend updates its read-model.

## Events
**Publishes** (`driver.events`): `driver.profile_updated`, `driver.pr_updated`,
`driver.history_appended`.
**Consumes**: `lap.recorded` (append history), `session.ended` (store summary),
`personal_record.broken` (confirm PR), `identity.resolved` (create record on new
`userId`).

## Key flow
1. New `userId` resolved → Driver upserts a racing profile record.
2. `lap.recorded` → append to history.
3. `session.ended` → store the per-session summary, emit `driver.history_appended`.
4. New best confirmed → `driver.pr_updated`.

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| `lap.recorded` for an unknown `userId` | Create a minimal racing profile on the fly (idempotent upsert); CRM/profile fills in later via events. |
| Conflicting racing-number edit from two sources | Driver owns racing fields → Driver's write wins (source-of-truth precedence); log the conflict. |
| Duplicate `lap.recorded` (redelivery) | Inbox dedupe by message id → no double-count. |
| RabbitMQ / peer down | Reads served from local store; writes/history queued via outbox. |
| Restart mid-session | Replay `lap.recorded`/`session.ended` from last marker; history rebuilt idempotently. |
