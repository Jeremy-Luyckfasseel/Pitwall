# Service: Timing

> Primary data source for the whole platform. Handles physical scanning and all lap
> calculations. Decisions: [Q&A](../00-questions-and-answers.md) Round 6.
> Conforms to the [service blueprint](../04-service-blueprint.md).

## Purpose
Detect drivers at the **entry gate**, time laps at the **start-finish line**, compute
lap times and detect personal records, and own the transponder→masterId mapping and the
development **simulator**.

## System of record (owns)
- Raw scan events (gate + line) and computed lap times.
- **Transponder (hardware-id) → `masterId`** mapping (assigned when a transponder is
  handed out at check-in).
- Local copies (ECST) of: `users` (keyed on `masterId`, for scan validation), each
  driver's **all-time PR** (seeded by Driver `driver.pr_updated`), and active session
  ids.

## Two scan points (distinct)
| Scan | Where | Meaning | Emits |
|---|---|---|---|
| **Check-in** | entry gate | identify driver, mark present | `driver.checked_in` |
| **Lap** | start-finish line | a crossing | `lap.recorded` |

QR codes embed the `masterId` (read directly, no lookup). Transponders carry a hardware
id resolved via the local mapping.

## Lap validity rules
- **Minimum lap time filter** (configurable, e.g. 10 s): crossings closer than that are
  treated as duplicate/bounce reads and ignored.
- **First crossing = start marker** (out-lap), not counted as a lap.
- Each subsequent valid crossing = exactly one lap; `lapTimeMs` = delta from previous
  valid crossing.

## Session lifecycle authority
Booking owns the *planned* schedule, but **Timing emits the ACTUAL** `session.started`
and `session.ended`. In **live operation** the operator starts the session via
`session.control_requested {action: start}` (race-control green-light) and ends it via
`{action: end}` or by planned-duration elapse; the **simulator** generates these
boundaries automatically ([ADR-0010](../../adr/0010-admin-operator-control-plane.md)).
Sad paths: start-already-started = no-op; end-never-started = graceful reject;
operator-end vs auto-end race = first wins. `session.ended` carries a summary so downstream
(Billing, Driver, Mailing, Leaderboard) can react. Booking reconciles its plan to these
actual times. A `lap.recorded` for a session Timing does not think is active is accepted
and logged (physical reality wins).

## Personal records
Timing keeps a local copy of each driver's all-time PR. On each lap it compares; if
beaten it publishes `personal_record.broken {masterId, sessionId, lapTimeMs,
previousMs}`. **Driver** is system-of-record and re-publishes the confirmed canonical
PR (`driver.pr_updated`), which Timing consumes to refresh its local copy.

## Simulator
Fully configurable (N drivers, randomized lap-time distribution, session length),
toggled via env/flag. Generates realistic gate + line scans so the whole platform runs
end-to-end with **no hardware**.

## Events
**Publishes** (`timing.events`): `driver.checked_in`, `lap.recorded`,
`session.started`, `session.ended`, `personal_record.broken`, `scanner.offline`,
`scanner.online`.
**Consumes**: `driver.pr_updated`, `driver.profile_updated` (refresh local user/PR
copies), `session.scheduled`/`session.rescheduled` (know the plan),
`session.control_requested` (operator start/end → emit the ACTUAL `session.started`/
`session.ended`), `identity.resolved` (when registering a walk-in token).

## Key flow (happy path)
1. Driver scans at gate → `driver.checked_in` (Billing opens a tab).
2. Each lap → persist locally (durable) + outbox → `lap.recorded` (Leaderboard,
   Driver react).
3. New best vs local PR → `personal_record.broken` (Mailing alerts, Driver confirms).
4. Session ends → `session.ended {summary}`.

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Scanner goes offline mid-session | Prior laps already persisted (persist-first) → never lost. Emit `scanner.offline`, flag a gap, alert Control Room. Missed crossings are acknowledged, **never faked**. Resume + `scanner.online` on recovery. |
| Duplicate/bounce read | Rejected by the minimum-lap-time filter. |
| Scan of an unknown token at the line (no resolved `masterId`) | **Register-first** (Q6.4, Round 19): every racer is resolved to a `masterId` at check-in **before** going on track, so this is an **operator-surfaced exception**, not a normal path — hold the scan locally, flag + alert Control Room (the person must complete check-in). **Never mint an id, never emit an anonymous lap, never drop it.** |
| RabbitMQ down (mid-session bus kill) | Laps still persisted locally + queued in the outbox; the Publisher's reconnect supervisor re-dials in-process (capped backoff) and re-declares the exchange + confirm channel, so the outbox **flushes automatically on recovery** with no loss (Story 1.10, NFR2). The heartbeat fails+skips while down, leaving the liveness file stale (honest bus-down signal). |
| Service restart mid-session | Replay from last-processed marker; idempotent inbox dedupes; local lap store intact. |
| Lap arrives for a session Timing thinks isn't active | Accept and log a warning; reconcile session state from the actual scan stream (physical reality wins). |
