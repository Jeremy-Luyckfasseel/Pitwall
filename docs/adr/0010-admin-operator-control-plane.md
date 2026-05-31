# ADR-0010 — Admin surface & operator control plane

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Rounds 13–14

## Context
The analysis phase left two things unstated: **what creates the session schedule**, and
**how a live session starts/ends** (Q6.5 listed "operator action" only as one possibility).
The PRD phase made both concrete and, in real karting, race control deliberately starts a
session — it doesn't begin from the first kart crossing.

## Decision
Introduce a small **operator/admin control plane**, kept inside the existing model (no new
service; admin lives in **Frontend**):

- **Admin role & UI.** The operator gains a **Frontend admin UI** alongside the Control Room
  dashboard, gated by a **separate admin login** (distinct from driver credentials). Admin
  accounts are **seeded from config/env** at deploy — no public admin signup.
- **Scheduling.** Booking seeds a **recurring daily schedule template**; the admin UI
  publishes `schedule.change_requested` to `frontend.events`, and Booking applies it
  (remaining the single authority on capacity + the reschedule cascade).
- **Operator-started sessions.** The operator publishes `session.control_requested
  {sessionId, action: start|end}` to `frontend.events`; **Timing** turns it into the ACTUAL
  `session.started` / `session.ended`. A live session also auto-ends on planned-duration
  elapse. The simulator generates these boundaries automatically.

All new operator/admin messages are **bus intents published to `frontend.events`** (the
originating exchange), consumed by the owning service — preserving "publish only to your own
exchange" and the bus-only rule. No new synchronous APIs.

## Consequences
- New contract events (`schedule.change_requested`, `session.control_requested`) and a new
  admin credential set in Frontend.
- Operator-started sessions need defined sad paths (start-already-started, end-never-started,
  end-vs-auto-end race) — recorded in Round 14 and the Timing/Frontend service docs.
- The "operator/admin" persona now spans two surfaces (Control Room read-only monitoring;
  Frontend admin write actions); whether one person fills both is the track's choice.
