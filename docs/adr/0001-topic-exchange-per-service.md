# ADR-0001 — One topic exchange per owning service

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Q1.1

## Context
Services are polyglot and must be loosely coupled; routing needs clear ownership and
flexible subscription. Options considered: a single shared topic exchange with a naming
convention, one exchange per owning service, or fanout-per-event.

## Decision
Each service **declares and owns exactly one durable `topic` exchange** named
`<service>.events`, and **publishes only to its own exchange**. Consumers bind their
own durable queues to the exchanges they care about, using `<entity>.<action>` routing
keys.

## Consequences
- Strong, obvious ownership: an event's origin is its exchange.
- Consumers opt in explicitly; adding a consumer never touches the producer.
- Slightly more exchange wiring than a single shared exchange — acceptable for the
  clarity gained.
- Pairs with durable queues, manual ack, and per-consumer DLX (see
  [ADR-0005](./0005-outbox-inbox-event-store.md)).
