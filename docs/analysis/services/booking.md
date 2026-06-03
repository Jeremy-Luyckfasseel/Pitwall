# Service: Booking

> Owns the session schedule and capacity. Decisions:
> [Q&A](../00-questions-and-answers.md) Round 8. Conforms to the
> [service blueprint](../04-service-blueprint.md).

## Purpose
Maintain the schedule of sessions/heats, track available capacity, confirm or reject
booking requests, and keep the schedule consistent when sessions run late.

## System of record (owns)
- **Sessions/heats**: planned start/end, capacity, current reservations.
- **Bookings**: who holds which slot.

## Schedule origin ([ADR-0010](../../adr/0010-admin-operator-control-plane.md))
Booking seeds a **default recurring daily schedule** (same session times/capacities each
day). The operator/admin overrides it via the Frontend admin UI, which publishes
`schedule.change_requested {action, sessionId?, times/capacity}`; Booking applies it
(create / reschedule / capacity / cancel) and emits the matching `session.*` event,
remaining the **single authority on capacity** and the reschedule cascade.

> Authority split: Booking owns the **plan**; Timing emits the **actual**
> `session.started`/`session.ended`. Booking reconciles its plan to reality.

## Booking flow & "session full"
Frontend publishes `booking.requested {requestId, masterId, sessionId}`. Booking is the
**single authority on capacity**: it atomically reserves a spot or rejects.
- Success → `booking.confirmed {bookingId, masterId, sessionId}` (Frontend, Mailing,
  Billing react).
- Full → `booking.rejected {requestId, reason:"full", alternatives:[…]}` offering the
  next available sessions. **Never a dead end.**

## Late-session reschedule cascade
When Timing reports a session ran past its slot (`session.ended` later than planned, or
a still-running session), Booking recalculates downstream start times, applies a
**configurable changeover buffer**, and publishes `session.rescheduled` for each
affected session. Mailing notifies affected bookers. Fully automatic.

## Events
**Publishes** (`booking.events`): `session.scheduled`, `session.rescheduled`,
`session.cancelled`, `booking.confirmed`, `booking.rejected`.
**Consumes**: `booking.requested` (Frontend intent), `schedule.change_requested` (admin
intent), `session.started`/`session.ended` (actuals from Timing → reconcile + trigger
cascade), `identity.resolved`.

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| Booking a full session | Reject with `alternatives[]` (next available). No dead end. |
| Two requests race for the last spot | Capacity check is atomic in Booking's store → one `confirmed`, one `rejected` with alternatives. |
| Session runs late | Auto-cascade downstream with buffer → `session.rescheduled`; Mailing notifies. |
| Reschedule would collide with another | Apply buffer + push the whole chain; if impossible within the day, mark overflow sessions and emit `session.rescheduled` with a flagged reason for operator visibility. |
| RabbitMQ / peer down | Schedule served from local store; confirmations queued via outbox. |
| Restart | Replay actual session events; reconcile plan from last marker. |
