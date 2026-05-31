# Pitwall Contract

The **only** thing shared between Pitwall's polyglot services. Every message on the bus
is JSON, wrapped in the common **envelope**, with its `data` payload validated against a
versioned **JSON Schema** here.

Full spec & event catalog: [`docs/analysis/02-message-bus-and-contracts.md`](../docs/analysis/02-message-bus-and-contracts.md).

## Layout
```
contract/
  README.md
  schemas/
    envelope.schema.json                     # the common envelope (every message)
    <service>/<entity>.<action>.v<N>.schema.json   # the data payload per event+version
  examples/
    <service>/<entity>.<action>.v<N>.example.json
```

## Rules
- **Validate both sides.** Producers validate before publishing; consumers validate on
  receipt. Invalid → log + dead-letter, never silently drop.
- **Envelope + payload.** A message must validate against `envelope.schema.json`, and
  its `data` must validate against the schema for its `type` + `schemaVersion`.
- **Versioning.** Additive (new optional fields) → no version bump; consumers are
  tolerant readers (ignore unknown fields). Breaking change → new `vN` schema + new
  routing key/version, run side by side during migration.
- **Consume by vendoring** (copy / git submodule path) into each service; do not fetch
  at runtime.

## How each service uses it
1. Load `envelope.schema.json` + the schema for the event it's handling.
2. Validate on publish and on receive.
3. Contract tests in CI assert every produced/consumed message matches its schema.
