# Architecture Decision Records

Short, numbered records of the *significant* architectural decisions and **why** they
were made. Seeded from the analysis-phase
[Questions & Answers log](../analysis/00-questions-and-answers.md).

Format: Context → Decision → Consequences. Status is `Accepted` unless noted.

| ADR | Title |
|-----|-------|
| [0001](./0001-topic-exchange-per-service.md) | One topic exchange per owning service |
| [0002](./0002-event-carried-state-transfer.md) | Event-carried state transfer, no inter-service APIs |
| [0003](./0003-identity-as-uuid-issuer.md) | Identity is a pure canonical-UUID issuer |
| [0004](./0004-bus-only-health-and-self-ping.md) | Bus-only health with control-room self-ping (no HTTP /health) |
| [0005](./0005-outbox-inbox-event-store.md) | Outbox + idempotent inbox + event store |
| [0006](./0006-json-schema-tolerant-reader.md) | JSON + JSON Schema contract with tolerant readers |
| [0007](./0007-monorepo-per-service-deploy.md) | Monorepo with per-service tag-driven deploys |
| [0008](./0008-single-bus-down-api-exception.md) | The single sanctioned bus-down API exception |
| [0009](./0009-data-governance.md) | Data governance & privacy as a first-class concern |
| [0010](./0010-admin-operator-control-plane.md) | Admin surface & operator control plane |
