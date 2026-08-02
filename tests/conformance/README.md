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
│   ├── crash-after-ack.yaml   # consumer crash+restart → replay, no double-count, no loss
│   └── heartbeat.yaml         # skeleton mechanics: heartbeat cadence/format + graceful shutdown
├── go/                   # the Go runner — its OWN go module
│   ├── scenario.go            # spec loader + model
│   ├── lane.go                # required/quarantine lane filter
│   └── *_integration_test.go  # the runner: broker, real-binary launcher, SSE/bus observer, scenarios
└── python/               # the Python runner — its OWN pip package (Story 3.1)
    ├── pitwall_conformance/scenario.py         # spec loader (the fields it needs)
    ├── pitwall_conformance/runner_helpers.py   # broker/process/bus-observer helpers
    └── tests/test_heartbeat.py                 # the runner: real Driver process + real RabbitMQ
```

Each language's runner reads the **same** `scenarios/*.yaml` — only `heartbeat` has a Python
implementation today (Driver has no domain logic until Story 3.2+, so the reliability
scenarios — peer-down, inbox-dup, publish-redeliver, crash-after-ack — stay Go-only, proven
against Timing/Leaderboard/Identity, until there is a Python consumer to exercise them
against).

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

The **Python runner** (`heartbeat` scenario only): starts a real `rabbitmq:4.3-management-alpine`
via **testcontainers-python**, runs the **real Driver process** (`python -m driver.main`) as a
subprocess pointed at that broker via env, and observes **raw bus traffic directly** — a
temporary exclusive queue bound to `driver.events` on the `control.heartbeat` routing key —
rather than a domain read-model (heartbeats aren't a domain event; there is no board to check).
It asserts at least `minCount` contract-valid heartbeats arrive within `windowMs`, each with a
fresh `at` timestamp, then sends `SIGTERM` and asserts a clean exit (code 0) within a bounded
time. The Go runner implements the identical scenario against Timing (simulator disabled, so
it's exercising nothing but the shared blueprint skeleton) via the same
exchange-declare/queue-declare/bind/consume shape, proving the two languages' skeleton
mechanics — not just unit-tested per language in isolation — are cross-language equivalent
(Story 3.1, AC4).

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

```sh
# Python runner (needs Docker):
pip install -e contract/codegen/python
pip install -e libs/py-pitwall
pip install -e services/driver
pip install -e tests/conformance/python
pytest tests/conformance/python/tests
```

**Windows caveat (not a service bug):** the graceful-shutdown assertion in `heartbeat` sends a
real `SIGTERM`, which neither Go's nor Python's standard library can deliver to an arbitrary
child process on Windows (`Process.Signal`/`os.kill` both report "not supported"). Both runners
detect this and `t.Skip`/`pytest.skip` **only that one assertion**, with a clear reason — the
heartbeat cadence/format proof still runs and gates locally on Windows exactly as it does in
CI. The authoritative gate for graceful shutdown is Linux CI (`ubuntu-latest`), where real
`SIGTERM` delivery works exactly as production Docker/Kubernetes termination does.

## A failing scenario means a SERVICE bug

The scenarios assert blueprint guarantees the services must uphold (M3/M4/M6/M7/M9). If one
fails, the defect is in a service — fix the **root cause** there (test-first). Never weaken a
scenario to make it green, and never `@skip` a flaky one — quarantine it.
