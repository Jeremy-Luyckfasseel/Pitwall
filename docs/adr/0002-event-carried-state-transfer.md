# ADR-0002 — Event-carried state transfer, no inter-service APIs

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Q1.3

## Context
A hard requirement: every service must keep functioning when its peers **or RabbitMQ
itself** are down. Synchronous request/response (HTTP APIs, or RPC-over-RabbitMQ)
fails this — a query has nothing to travel over when the bus or peer is unavailable.

## Decision
Use **event-carried state transfer (ECST)**. Producers publish the full relevant state
in events; each consumer maintains its **own local copy** (read-model) of just the
slice it needs, keyed on the canonical `masterId`. **No synchronous cross-service calls**
as a primary pattern (no HTTP APIs, no RPC-over-bus). Reads always hit the local copy.

## Consequences
- Each service keeps serving local reads/writes during any outage; outbound events
  queue in the outbox and flush on recovery (see
  [ADR-0005](./0005-outbox-inbox-event-store.md)).
- The system is **eventually consistent** — explicitly accepted.
- Some data is duplicated across services' local copies — accepted as the cost of
  decoupling and availability.
- The only exception to "no APIs" is the bus-down alert path
  ([ADR-0008](./0008-single-bus-down-api-exception.md)).
