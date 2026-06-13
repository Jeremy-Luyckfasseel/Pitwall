# Cross-language conformance harness + e2e smoke

This is the **4th CI test layer** (NFR23) and Epic 1's **e2e skeleton** (AR16): one
language-neutral scenario spec, run by a thin per-language runner that drives the **real
built service binaries** against **one shared real RabbitMQ**, asserting **identical
observable outcomes**. Per AR16 the conformance harness *is* the e2e smoke — the happy-path
smoke is just the `smoke` scenario alongside the four reliability scenarios.

> Execution model + layout were decided in Q&A **Round 28** (real binaries + testcontainers;
> this own Go module). See `docs/analysis/00-questions-and-answers.md#round-28`.

## Layout

```
tests/conformance/
├── scenarios/            # the language-neutral spec (YAML data, no code)
│   ├── smoke.yaml             # happy path: simulated laps → board → session end
│   ├── peer-down.yaml         # bus down mid-session → buffer, freeze stale, reconverge
│   ├── inbox-dup.yaml         # duplicate lap.recorded (same envelope id) → no-op
│   ├── publish-redeliver.yaml # redelivered persistent lap → applied exactly once
│   └── crash-after-ack.yaml   # consumer crash+restart → replay, no double-count, no loss
└── go/                   # the Go runner — its OWN go module (Go for now)
    ├── scenario.go            # spec loader + model
    ├── lane.go                # required/quarantine lane filter
    └── *_integration_test.go  # the runner: broker, real-binary launcher, SSE observer, scenarios
```

A later language joins a sibling `tests/conformance/<lang>/` runner against the **same**
`scenarios/*.yaml`.

## How it runs

The Go runner (`-tags=integration`):

1. starts one shared `rabbitmq:4.3-management-alpine` via **testcontainers** (a fixed host
   port so a state-preserving Stop→Start returns the same broker at the same address);
2. `go build`s and runs the **real Timing (simulator mode, seed-deterministic) and
   Leaderboard binaries** as subprocesses pointed at that broker via env;
3. asserts **observable** outcomes — primarily the **served board** over the Leaderboard
   SSE `/events` stream, plus process state.

For the consumer-focused scenarios the harness itself is the producer, publishing
`/contract`-valid `lap.recorded` / `session.started` envelopes. Dedupe is made **falsifiable
on the board** by republishing a duplicate under the **same envelope id but a faster time**:
if the inbox dedupes on envelope id, the board keeps the original (slower) time.

## No-sleep discipline (hard rule, AR16)

Every wait polls a real **observable condition** (`waitUntil`) — the served board, process
readiness. A `time.Sleep` is only ever a **poll interval**, never an assertion of timing.

## Lanes — quarantine, never `@skip` (AR16)

A scenario marked `quarantine: true` in its YAML runs in the **quarantine lane**
(`CONFORMANCE_LANE=quarantine`), which is **non-blocking** in CI but **still executes** the
scenario — a flaky scenario is *routed here*, never `t.Skip`-ped or deleted. The **required**
lane (default) is the merge gate and runs every non-quarantined scenario. For Epic 1 the
quarantine lane is empty (zero flaky scenarios); the lane exists as a mechanism.

## Running it

```sh
make smoke              # required lane — the merge gate (needs Docker)
make smoke-quarantine   # quarantine lane — non-blocking

# directly:
cd tests/conformance/go
go test ./...                                          # unit (loader + lane filter, no Docker)
go test -tags=integration -timeout 900s ./...          # full conformance + e2e smoke (Docker)
go test -tags=integration -run TestConformance/smoke   # one scenario
```

On Windows, testcontainers needs Docker Desktop running + `TESTCONTAINERS_RYUK_DISABLED=true`.

## A failing scenario means a SERVICE bug

The scenarios assert blueprint guarantees the services must uphold (M3/M4/M6/M7/M9). If one
fails, the defect is in a service — fix the **root cause** there (test-first). Never weaken a
scenario to make it green, and never `@skip` a flaky one — quarantine it.
