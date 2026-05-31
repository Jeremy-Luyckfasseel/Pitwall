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
| Missing nickname | Fall back to a racing number or a short form of `userId`; update when `driver.profile_updated` arrives. |
| Service restart mid-session | Rebuild the board by replaying the session's events from the last marker. |
| RabbitMQ down | Display freezes on last-known standings (clearly the safe degradation); catches up on reconnect. |
