# ADR-0006 — JSON + JSON Schema contract with tolerant readers

Status: Accepted · Source: [Q&A](../analysis/00-questions-and-answers.md) Q1.2, Q3.5

## Context
The message contract is the only coupling point in a polyglot system, so it must
validate well and be easy to consume from any language. XML+XSD was considered but its
tooling is weak outside Java/.NET.

## Decision
Messages are **JSON**, wrapped in a standard envelope, and validated against
**JSON Schema** files kept in a **`/contract` folder in this repo** (not a separate
repo). Both producers and consumers validate. Schema evolution: each event carries an
integer `schemaVersion`; additive changes don't bump it and consumers are **tolerant
readers** (ignore unknown fields); breaking changes get a new routing key/version and
run side by side during migration.

## Consequences
- First-class validation in every candidate language.
- Contract tests in CI fail on any drift between code and `/contract`.
- Tolerant readers + versioned breaking changes allow independent service evolution.
- The contract is vendored/submoduled into each service rather than fetched at runtime.
