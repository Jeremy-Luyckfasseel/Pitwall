# Service: Control Room

> Monitors every service and the bus itself, surfaces live status + stats, and fires
> alerts. A **custom lightweight dashboard** (no ELK). Decisions:
> [Q&A](../00-questions-and-answers.md) Round 4 + **Round 18**,
> [ADR-0004](../../adr/0004-bus-only-health-and-self-ping.md) +
> [ADR-0012](../../adr/0012-control-room-observability-and-ai-agent.md). Conforms to the
> [service blueprint](../04-service-blueprint.md) (with the monitoring-specific notes
> below). The dashboard's UX is designed in the Surface-4 UX run (5 views: Overview ·
> Health · Live · **Message Flow** · Alerts).

## Purpose
Be the single pane of glass for "is everything alive?" and "what's happening right
now?", and alert immediately when something goes down.

## Three-layer health model (bus-only — no HTTP)
1. **Per-service heartbeats**: every service publishes a `heartbeat` over the bus every
   1 s. The Control Room tracks each service's last-seen time.
2. **Self-ping**: the Control Room publishes `control.selfping` through RabbitMQ every
   1 s and listens for it. If it doesn't come back → **RabbitMQ is down**.
3. **Disambiguation**:
   - self-ping returns **but** a service's heartbeat is missing → *that service* is down;
   - self-ping missing → *the bus* is down.
4. **Down threshold**: **3 consecutive missed heartbeats (~3 s)** → mark DOWN + alert.

## Dashboard
- **Live status** (online/offline) per service.
- **Stats**: active drivers, sessions today, laps recorded — built into the Control
  Room's **own read-model** by subscribing to domain events (`lap.recorded`,
  `session.started`, driver/check-in events).
- **Alert history** (persisted).
- **Message Flow** (Round 18): a live service-to-service event-flow diagram, fed by the
  metadata-only observation tap below.
- Live updates via websockets/SSE.

## Message-flow observability ([Round 18](../00-questions-and-answers.md), [ADR-0012](../../adr/0012-control-room-observability-and-ai-agent.md))
A passive **metadata-only observation tap** (a wildcard/firehose bind or the broker's
firehose tracer) feeds the Message Flow view. It records only **routing metadata —
`{from-service, event-type, consumers, timestamp}` — never message bodies/payloads**, so
**no PII crosses the tap**. The stream is **sampleable** under load and **not persisted**
beyond the rolling live window. Full-payload observation is explicitly out of scope. This
is a read-only observer — it publishes nothing and adds no new contract event.

## AI remediation agent — POST-MVP, external ([Round 18](../00-questions-and-answers.md), [ADR-0012](../../adr/0012-control-room-observability-and-ai-agent.md))
A **deferred** capability, **not built in the MVP** and **not an 11th service**: an
**external ops/dev tool** (like CI/CD or the simulators) that consumes `alert.raised`
(plus read-only context: logs / event store / the tap above) to diagnose a fault and
**open a human-reviewed GitHub PR** (or write up likely causes). Guardrails: **scoped
GitHub token, never auto-merge/deploy** — *propose, never dispose*. Its only Control Room
footprint is a **read-only one-line hint** per alert (diagnosis + a "proposed PR"
deep-link to the agent's own page). GitHub is a fifth **external edge** (like VIES / SMTP /
the PSP), so bus-only between services is preserved.

## Alerting
- Normal: show on dashboard immediately **and** publish `alert.raised` → Mailing emails
  it.
- **Bus-down exception**: when RabbitMQ itself is down (self-ping fails), the bus can't
  carry the alert, so the Control Room makes a **direct API call to Mailing** to send
  the "RabbitMQ is down" alert. **This is the only sanctioned cross-service API in the
  whole system** — used for nothing else. See
  [ADR-0008](../../adr/0008-single-bus-down-api-exception.md).

## System of record (owns)
- Live status table (last-seen per service).
- Stats read-model.
- Alert history.
- **Privacy-saga tracking** (erasure completion + export-bundle assembly) — see below.

## Privacy coordination ([ADR-0009](../../adr/0009-data-governance.md))
As the existing cross-cutting aggregator, the Control Room also coordinates the privacy
saga (a deliberate reuse, **no new service**):
- **Erasure:** consumes every service's `privacy.erased {masterId, service, mode}` and tracks
  completion across all services on the dashboard (N/N done).
- **Export:** consumes each service's `privacy.data_provided` slice, **assembles** the
  bundle, and emits `privacy.export_ready {masterId, documentRef}` for Mailing to deliver.

## Events
**Publishes** (`control.events`): `control.selfping`, `alert.raised`, `alert.cleared`,
`privacy.export_ready`.
**Consumes**: `heartbeat` from all services, `control.selfping` (its own), domain
events for stats, and `privacy.erased` / `privacy.data_provided` from every service.
**Observes** (Round 18, metadata only — no new event): routing metadata across the bus to
feed the Message Flow view (`{from, event-type, consumers, timestamp}`, never payloads).

## Note vs the brief
The brief said the Control Room "does not sit on the message bus" and polls HTTP
health directly. Pitwall **deliberately overrides** this: everything is bus-based, and
the Control Room *is* a bus participant. The brief's goal (detect a silent service) is
still met via the self-ping disambiguation. See
[ADR-0004](../../adr/0004-bus-only-health-and-self-ping.md).

## Sad-path table
| Scenario | Handled outcome |
|---|---|
| A service goes silent | After 3 missed heartbeats, mark DOWN, raise an alert, record in history. |
| RabbitMQ down | Self-ping fails → raise a bus-down alert via the **direct Mailing API**; dashboard shows global outage. |
| Control Room itself restarts | Rebuild status from incoming heartbeats; rebuild stats by replaying domain events from the last marker. |
| False alarm (brief blip) | The 3-miss threshold tolerates 1–2 transient drops before alerting. |
| Mailing down when alerting | Alert still shows on the dashboard + history; email retried when Mailing returns. |
