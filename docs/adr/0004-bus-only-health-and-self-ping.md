# ADR-0004 — Bus-only health with control-room self-ping (no HTTP /health)

Status: Accepted (overrides the brief) · Source:
[Q&A](../analysis/00-questions-and-answers.md) Q2.2, Q4.1–4.4

## Context
The brief mandates an HTTP `/health` endpoint per service and a control room that polls
health **directly, not on the bus**. The user instead wants **everything over
RabbitMQ** — no HTTP endpoints at all.

## Decision
**No HTTP `/health` endpoints.** A three-layer, bus-only health model:
1. Every service publishes a **heartbeat every 1 s** to the Control Room.
2. The Control Room publishes a **self-ping every 1 s** through RabbitMQ and listens
   for it; no return → **bus is down**.
3. Disambiguation: self-ping returns but a heartbeat is missing → that *service* is
   down; self-ping missing → the *bus* is down.
4. **3 missed heartbeats (~3 s)** → mark DOWN + alert.
The Control Room builds its stats read-model by subscribing to domain events. It is a
**custom lightweight dashboard** (ELK rejected for RAM + weak real-time alerting).
Docker `healthcheck:` uses a **bus-connectivity script / liveness touch-file**, not
HTTP.

## Consequences
- Deliberate divergence from the brief; the brief's *goal* (detect a silent service) is
  still met via self-ping disambiguation.
- The Control Room **is** a bus participant (also contrary to the brief's wording).
- Trade-off: no out-of-band check — a process alive but with a wedged bus client looks
  "down". Accepted.
- The bus-down alert needs a non-bus path ([ADR-0008](./0008-single-bus-down-api-exception.md)).
