# Service: Leaderboard

> A live, trackside read-model of current standings. Decisions:
> [Q&A](../00-questions-and-answers.md) Round 10. Conforms to the
> [service blueprint](../04-service-blueprint.md).

## Purpose
Show live standings ordered by best lap during an active session, on screens at the
track, independently of the Frontend.

## System of record (owns)
Nothing authoritative — it is a **pure read-model** rebuilt from events. It keeps a
local copy of the current session's standings + leaderboard nicknames (from Driver).

## Behaviour
- **Ordering**: by best lap time (ascending).
- **Live update**: instantly on each `lap.recorded`.
- **Reset**: automatically on `session.started` (clears the board for the new session).
- **Tie-break**: whoever set the equal time **first** ranks higher.
- **Status**: shows session status (active / finished) from `session.started`/
  `session.ended`.
- **Display name**: leaderboard nickname from `driver.profile_updated`.

## Events
**Consumes**: `lap.recorded`, `session.started`, `session.ended`,
`driver.profile_updated` (nicknames). **Publishes**: none required (optionally a
`leaderboard.updated` for other displays).

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Lap arrives before `session.started` seen | Start an implicit board keyed on the lap's `sessionId`; reconcile when the start event arrives. |
| Out-of-order / duplicate laps | Idempotent by message id; standings recomputed from the best-known set. |
| Invalid message on consume (fails `/contract`, or blank `sessionId`) | **Parked immediately** in `leaderboard.lap-recorded.parking` + alert — never retried as poison, never applied (Story 1.9, M5). |
| Processing failure (transient or genuine poison) | **TTL-retry with exponential backoff** via `leaderboard.lap-recorded.retry` (1s→2s→4s→8s); clears within the cap → applied once; exceeds the 5-attempt cap → **parked + Control-Room alert** (Story 1.9, NFR4/NFR6). |
| Missing nickname | Fall back to a racing number or a short form of `masterId`; update when `driver.profile_updated` arrives. |
| Service restart mid-session | The read-model is **durable** (SQLite) — not rebuilt from scratch. The idempotent inbox is the last-processed marker; the durable work queue redelivers the unacked tail, so the restart replays past the marker with **no double-count (M6)** and **no loss (M4)** (Story 1.10). |
| RabbitMQ down (mid-session bus kill) | Display **freezes on last-known standings**; the served bundle flips **`stale`/`reconnecting`** (flagged, never faked-live — FR47/C1). A reconnect supervisor re-dials with capped backoff and re-declares topology; on restore the flag clears and the board **reconverges ≤ 10 s** (Story 1.10, M9). |
