# ADR-0008 — The single sanctioned bus-down API exception

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Q4.2

## Context
Pitwall forbids inter-service APIs ([ADR-0002](./0002-event-carried-state-transfer.md)).
But the one alert that matters most — **"RabbitMQ is down"** — cannot be delivered over
the bus, because the bus is exactly what's broken.

## Decision
Allow **exactly one** direct, non-bus call in the whole system: when the Control Room's
self-ping detects RabbitMQ is down, it makes a **direct API call to Mailing** to send
the bus-down alert email. This exception is:
- **Single-purpose**: bus-down alerts only.
- **One direction**: Control Room → Mailing only.
- **Forbidden everywhere else** — any other inter-service API is a design violation.

## Consequences
- The most critical failure still notifies a human even when the bus is dead.
- A clearly bounded, documented carve-out that won't erode the no-APIs rule.
- Mailing must expose this one minimal endpoint and nothing more; the Control Room must
  use it for nothing else.
