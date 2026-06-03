# ADR-0013 — Admin AI Assistant (read-model-backed analytics + bus-intent writes)

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Round 19 ·
Originated in the `bmad-create-architecture` workflow (2026-06-03). ·
**Scope amended Round 20 (2026-06-04): the entire assistant is post-MVP — see §4.**

## Context
The operator wants an **admin AI chatbot**: ask analytics questions ("how many drivers today",
"revenue this month") and, later, perform admin operations (create users, add a driver to a session,
cancel, …) — "as much control as possible". Two non-negotiables are in tension with that wish:

1. **Bus-only between services** (no synchronous inter-service APIs / RPC; ECST; works when peers or
   the bus are down — [ADR-0002](./0002-event-carried-state-transfer.md)). A naive "MCP into every
   service" would reintroduce exactly the synchronous coupling the platform exists to avoid, and a
   read fan-out would die whenever a service is down.
2. **"10 services + Control Room — no 11th service"** (CLAUDE.md).

It must also be distinguished from the **Round-18 GitHub remediation bot**
([ADR-0012](./0012-control-room-observability-and-ai-agent.md)): that one is *propose-never-dispose*
on the GitHub edge; this one *executes* admin operations. They are **different actors with opposite
safety postures** and must not be merged. The overriding design goal: the assistant's answers must be
**always as correct as possible**.

## Decision
Introduce the **Admin AI Assistant** as an **external ops tool** — outside the service count, the same
category as CI/CD or the Timing/Bar simulators — so the "no 11th bus service" rule holds verbatim.

**1. The LLM never computes or invents a number.** It maps natural language onto a **fixed, typed,
versioned set of query functions**; the figures come from deterministic queries. The model is fenced
by that tool surface (no free-form SQL), which is the primary correctness lever and the part that is
unit/contract-testable (golden NL→query fixtures; the model itself is not tested).

**2. Reads come from a dedicated CQRS reporting read-model**, a bus consumer built with the same
ECST + idempotent-inbox machinery as every service (owns no canonical domain state, **emits no domain
events** — a read-only derived projection, *not* a service). It carries a **`last-synced` watermark**
and **flags lag / bus-down** rather than presenting stale data as live (counter-metric C1). The
assistant reaches it over **one read-only MCP edge**. **MCP-to-every-service is rejected** — live
fan-out is *less* correct (each service would aggregate differently), slower, and breaks read-path
fault tolerance.

**3. Writes (phase two) publish the existing admin intents on `frontend.events`** — the *same*
intents the Frontend admin UI already emits (`schedule.change_requested`, `session.control_requested`,
profile/booking intents; ADR-0010). The assistant is a **natural-language skin over the bus**, not a
new write path and not a bus bypass — so the bus-only rule is **preserved, not excepted**.
**Destructive operations (erase / cancel / refund) require an explicit human confirm.** Every
AI-issued action is contract-validated like any other producer and **audited** with `correlationId` +
the acting identity (the AI **and** the invoking admin), reusing the DG-4 audit trail.

**4. Scope & sequencing.** *(Amended 2026-06-04, [Q20.1](../analysis/00-questions-and-answers.md).)* The
**entire** assistant is **post-MVP**: read analytics is the **first** post-MVP increment, writes-via-intents
(with confirm) the **second** — neither is in the MVP (the MVP is the 10 services + Control Room, Q19.8).
The assistant carries no FR number. Its only MVP footprint is the Control Room's read-only one-line **AI
hint** (designed in epics Epic 12). The reporting read-model + MCP server + LLM provider (behind a
swappable **port**, Anthropic SDK v1) live under `tools/admin-ai-assistant/` and ship later. *(Supersedes
the earlier "Read analytics = MVP" reading.)*

## Consequences
- **No new bus service and no inter-service API:** the assistant is an external-edge actor; the
  reporting read-model is a read-only CQRS consumer. The "10 + Control Room" count is unchanged.
- **No new contract event** for reads (it consumes existing domain events into its projection); writes
  reuse the existing `frontend.events` admin intents.
- **Correctness posture:** deterministic query functions + watermarked, honestly-degraded projection →
  correct when fresh, *honest* when not, never confidently wrong.
- **Safety boundary:** reads are free; destructive writes are human-in-the-loop. Distinct from the
  ADR-0012 GitHub bot (propose-never-dispose). When the write phase is built it needs its own
  authorization/threat review.
- **Open (build-time):** whether the reporting read-model is a standalone tool or folded into the
  Control Room's read-model (default: standalone, for query coverage + ops-view separation); the exact
  query-function catalog.
