# ADR-0005 — Outbox + idempotent inbox + event store

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Q1.4, Q10.2

## Context
Events must never be lost, even when RabbitMQ or a consumer is down, and a restarting
service must catch up on what it missed.

## Decision
Combine four mechanisms across every service:
- **Durable queues + persistent messages + manual ack** after successful processing.
- **Dead-letter queue + retry/backoff** for poison messages.
- **Outbox** (producer): the state change and the outgoing event are committed in one
  local transaction; a relay publishes to RabbitMQ and retries until reachable.
- **Idempotent inbox** (consumer): dedupe by message `id`; reprocessing is a no-op.
- **Event store**: persist every event to a durable log; a restarting service replays
  from its **last-processed marker** to catch up. Read-models can fully rebuild.

## Consequences
- Survives bus-down (outbox), consumer crash mid-processing (manual ack), duplicates
  and replay (inbox), and restarts (event-store replay).
- Each service carries outbox/inbox/event-store tables in its private DB.
- Implementation is per-language but standardised by the
  [service blueprint](../analysis/04-service-blueprint.md).
