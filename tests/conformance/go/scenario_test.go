package conformance

import (
	"path/filepath"
	"testing"
)

// The loader is broker-free unit logic: it parses the language-neutral
// scenarios/*.yaml spec into the model the runner dispatches on. These tests
// pin the spec format (AC1: "a single YAML scenario spec").

func TestLoadScenario_Smoke(t *testing.T) {
	s, err := LoadScenario(scenarioPath(t, "smoke.yaml"))
	if err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	if s.Name != "smoke" {
		t.Errorf("Name = %q, want smoke", s.Name)
	}
	if s.Quarantine {
		t.Error("smoke must not be quarantined")
	}
	if s.Simulator == nil {
		t.Fatal("smoke must declare a simulator block")
	}
	if s.Simulator.Drivers < 1 || s.Simulator.SessionLaps < 1 {
		t.Errorf("simulator drivers/sessionLaps must be >=1, got %d/%d", s.Simulator.Drivers, s.Simulator.SessionLaps)
	}
	if !s.Expect.RankedAscending {
		t.Error("smoke must expect rankedAscending")
	}
	if !s.Expect.SessionFinished {
		t.Error("smoke must expect sessionFinished")
	}
	if s.Expect.BoardDrivers != s.Simulator.Drivers {
		t.Errorf("expect.boardDrivers (%d) should equal simulator.drivers (%d)", s.Expect.BoardDrivers, s.Simulator.Drivers)
	}
}

func TestLoadScenario_InboxDup_FixtureWithDuplicate(t *testing.T) {
	s, err := LoadScenario(scenarioPath(t, "inbox-dup.yaml"))
	if err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	if s.Fixture == nil {
		t.Fatal("inbox-dup must declare a deterministic lap fixture")
	}
	if len(s.Fixture.Laps) < 2 {
		t.Fatalf("inbox-dup needs >=2 laps, got %d", len(s.Fixture.Laps))
	}
	if s.Fixture.DuplicateIndex == nil {
		t.Fatal("inbox-dup must name a duplicateIndex (the lap republished under the same envelope id)")
	}
	if *s.Fixture.DuplicateIndex < 0 || *s.Fixture.DuplicateIndex >= len(s.Fixture.Laps) {
		t.Errorf("duplicateIndex %d out of range", *s.Fixture.DuplicateIndex)
	}
	// The duplicate must carry a FASTER time so dedupe is falsifiable on the board.
	if s.Fixture.DuplicateLapMs == nil {
		t.Fatal("inbox-dup must set duplicateLapMs (a faster time for the duplicate)")
	}
	if *s.Fixture.DuplicateLapMs >= s.Fixture.Laps[*s.Fixture.DuplicateIndex].LapMs {
		t.Errorf("duplicateLapMs %d must be faster than the original %d",
			*s.Fixture.DuplicateLapMs, s.Fixture.Laps[*s.Fixture.DuplicateIndex].LapMs)
	}
}

func TestLoadAll_CoversTheFourReliabilityScenariosPlusSmoke(t *testing.T) {
	all, err := LoadAll(scenarioDir(t))
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	// AC1: publish-redeliver, inbox-dup, crash-after-ack, peer-down + the smoke (AC2).
	want := []string{"crash-after-ack", "inbox-dup", "peer-down", "publish-redeliver", "smoke"}
	got := map[string]bool{}
	for _, s := range all {
		got[s.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("scenario spec is missing %q", name)
		}
	}
	if len(all) != len(want) {
		t.Errorf("loaded %d scenarios, want %d (%v)", len(all), len(want), want)
	}
}

func TestLoadAll_SortedByNameForDeterministicOrder(t *testing.T) {
	all, err := LoadAll(scenarioDir(t))
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Name > all[i].Name {
			t.Errorf("scenarios not sorted by name at %d: %q > %q", i, all[i-1].Name, all[i].Name)
		}
	}
}

// scenarioDir resolves the committed scenarios/ directory relative to this module.
func scenarioDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "scenarios")
}

func scenarioPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(scenarioDir(t), name)
}
