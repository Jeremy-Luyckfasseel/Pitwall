# ADR-0012 — Control Room observability tap & the (deferred) external AI agent

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Round 18 ·
PRD FR92–FR94. Originated from the **Surface 4 (Control Room) UX run** (decisions D8 + D12).

## Context
The Control Room UX run added two capabilities beyond the Round 4 design, each of which brushes
a non-negotiable and so was **flagged for an explicit decision, not assumed**:

1. A live **Message Flow** view — a service-to-service event-flow diagram. But the Control Room
   only consumes heartbeats, stats domain events, privacy events, and its own self-ping
   ([ADR-0004](./0004-bus-only-health-and-self-ping.md)); it does **not** observe arbitrary
   inter-service traffic. Feeding the diagram needs a new observation mechanism, which raises
   PII / volume / retention questions.
2. An **AI remediation agent** that, on an alert, diagnoses the fault and opens a fix PR (or
   writes up likely causes). This collides with **"10 services + Control Room — no 11th service"**
   and means an AI acting on the codebase, which is safety-sensitive.

## Decision

**Message Flow observability — a metadata-only tap (in scope).** The Control Room gains a passive
**observation tap** (a wildcard/firehose bind or the broker's firehose tracer) that records only
**routing metadata — `{from-service, event-type, consumers, timestamp}` — never message
bodies/payloads**. No PII crosses the tap; the stream is **sampleable** under load and **not
persisted** beyond the rolling live window. The diagram is **logical** (publisher→consumer), with
the honest caption that everything physically routes via RabbitMQ ([ADR-0001](./0001-topic-exchange-per-service.md)).
**Full-payload observation is rejected** (PII, volume, retention).

**AI remediation agent — a deferred, external ops tool (post-MVP).** The agent is **not** a Pitwall
bus service. It is modeled as an **external ops/dev tool outside the service count** — the same
category as CI/CD or the Timing/Bar simulators — so the "10 services + Control Room" rule holds
**verbatim**. It **observes** `alert.raised` (plus read-only diagnostic context: logs / event store
/ the FR93 flow metadata) and **acts on the GitHub external edge** — a fifth external edge, like
Frontend⇄browsers, POS→VIES, Mailing→SMTP, and the PSP payments edge ([ADR-0011](./0011-external-payments-edge.md)).
It is **read-diagnose-suggest only**: it may **open a pull request or issue** for **human review
and merge**, running with a **least-privilege scoped GitHub token** (open PR/issue — no merge, no
force-push, no deploy), and **never auto-merges to production**. Its only Control Room footprint is
a **read-only one-line hint** per alert (diagnosis + a "proposed PR" deep-link to the agent's own
page). The capability is **decided and documented now but built later** — the MVP is unchanged.

Rejected alternatives: an **11th first-class bus service** (breaks the non-negotiable) and **folding
the agent into the Control Room** (the Control Room is observe-only/read-only; the agent acts on
GitHub).

## Consequences
- The Control Room's documented role extends to a **read-only metadata observer**; its service doc
  and the `02` catalog gain a note for the observation stream (no new domain event — `alert.raised`
  already exists, FR74).
- The metadata tap must guarantee **no payload/PII capture** and bounded volume (sampling) — a
  build-time constraint, and a data-governance check ([ADR-0009](./0009-data-governance.md)).
- The AI agent is a **post-MVP** line item with a hard safety boundary (**propose, never dispose**;
  scoped token; human-reviewed PRs only). When built, it needs its own threat-model / authorization
  review; it is explicitly **not** an autonomous remediation system.
- "10 services + Control Room — no 11th service" and "bus-only between services" both remain intact;
  the AI agent is an external edge actor, not an inter-service API.
