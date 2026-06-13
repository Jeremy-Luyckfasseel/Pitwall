// Package conformance is the cross-language conformance harness + e2e smoke
// (Story 1.11, AR16). It reads the language-neutral scenarios/*.yaml spec and a
// thin Go runner drives the REAL built service binaries (Timing simulator +
// Leaderboard) against one shared real RabbitMQ, asserting identical observable
// outcomes — waiting on observable conditions, never sleeping. Per AR16 this
// harness IS the e2e skeleton: the happy-path smoke is just the `smoke` scenario
// alongside the four reliability scenarios (publish-redeliver, inbox-dup,
// crash-after-ack, peer-down). The execution model + layout were decided in Q&A
// Round 28 (real binaries + testcontainers; this own go module).
package conformance

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Scenario is one entry in the spec. The fields are language-neutral DATA: a
// future tests/conformance/<lang>/ runner reads the SAME YAML and implements the
// same mechanism in its language. The Go runner dispatches by Name to a scenario
// implementation that consumes these parameters + expectations.
type Scenario struct {
	// Name is the dispatch key and the scenario file's logical id.
	Name string `yaml:"name"`
	// Title/Description document the scenario for humans (and any reader of the spec).
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// Proves lists the metric guarantees this scenario exercises (e.g. M6), for traceability.
	Proves []string `yaml:"proves"`
	// Quarantine routes a flaky scenario to the non-required quarantine lane —
	// never @skip (AR16). Defaults false; for Epic 1 every scenario is false.
	Quarantine bool `yaml:"quarantine"`

	// Simulator, when present, declares that this scenario drives the REAL Timing
	// binary in simulator mode (seed-deterministic) as the lap producer (smoke, peer-down).
	Simulator *SimulatorSpec `yaml:"simulator"`
	// Fixture, when present, declares a deterministic lap fixture the harness
	// publishes itself (consumer-focused scenarios: inbox-dup, publish-redeliver, crash-after-ack).
	Fixture *FixtureSpec `yaml:"fixture"`

	// Expect is the observable outcome the runner asserts on the served board.
	Expect ExpectSpec `yaml:"expect"`
}

// SimulatorSpec mirrors the Timing simulator's data-shaping knobs (the
// language-neutral subset the harness sets as env on the real binary).
type SimulatorSpec struct {
	Drivers     int   `yaml:"drivers"`
	SessionLaps int   `yaml:"sessionLaps"`
	Seed        int64 `yaml:"seed"`
	// TickMs paces the simulator (SIM_TICK_MS). Defaults to a fast 10 ms; a
	// scenario that needs the session to SPAN a mid-session event (e.g. peer-down
	// must kill the bus while laps are still being produced) sets it higher.
	TickMs int `yaml:"tickMs"`
}

// FixtureSpec is a deterministic, language-neutral lap sequence the harness
// publishes directly (as the producer) so consumer-side guarantees are exact.
type FixtureSpec struct {
	Session string    `yaml:"session"`
	Laps    []LapSpec `yaml:"laps"`
	// DuplicateIndex names the lap that is republished under the SAME envelope id
	// to probe idempotency. The duplicate carries DuplicateLapMs (a FASTER time)
	// so dedupe is falsifiable ON THE BOARD: if the inbox dedupes on envelope id,
	// the board keeps the ORIGINAL (slower) time; if it failed, the faster time
	// would show. nil ⇒ no duplicate probe.
	DuplicateIndex *int   `yaml:"duplicateIndex"`
	DuplicateLapMs *int64 `yaml:"duplicateLapMs"`
	// AckedBeforeCrash (crash-after-ack): how many of Laps are published + applied
	// before the consumer process is killed and restarted on the same DB.
	AckedBeforeCrash *int `yaml:"ackedBeforeCrash"`
}

// LapSpec is one fixed lap: a driver's masterId + lap time in ms.
type LapSpec struct {
	Master string `yaml:"master"`
	LapMs  int64  `yaml:"lapMs"`
}

// ExpectSpec is the observable board outcome the runner asserts (via the
// Leaderboard SSE snapshot) once the scenario's actions are applied.
type ExpectSpec struct {
	BoardDrivers    int  `yaml:"boardDrivers"`
	RankedAscending bool `yaml:"rankedAscending"`
	SessionFinished bool `yaml:"sessionFinished"`
}

// LoadScenario parses one scenarios/*.yaml file into a Scenario.
func LoadScenario(path string) (Scenario, error) {
	var s Scenario
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("read scenario %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // reject unknown fields so the spec format stays honest
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("parse scenario %s: %w", path, err)
	}
	if s.Name == "" {
		return s, fmt.Errorf("scenario %s has no name", path)
	}
	return s, nil
}

// LoadAll parses every scenarios/*.yaml in dir, sorted by name for deterministic
// run order.
func LoadAll(dir string) ([]Scenario, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("glob scenarios: %w", err)
	}
	out := make([]Scenario, 0, len(matches))
	for _, m := range matches {
		s, err := LoadScenario(m)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
