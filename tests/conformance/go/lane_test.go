package conformance

import "testing"

// The quarantine lane (AR16: a flaky scenario goes to a quarantine lane, NEVER
// @skip). Lane routing is a pure filter on the spec's quarantine flag: the
// required lane runs the non-quarantined scenarios (the merge gate); the
// quarantine lane runs the quarantined ones (non-blocking) — they are never
// skipped, just executed in a different lane. These unit tests prove the
// mechanism without needing an actually-flaky scenario.

func TestScenariosForLane_RequiredExcludesQuarantined(t *testing.T) {
	all := []Scenario{
		{Name: "smoke", Quarantine: false},
		{Name: "flaky-thing", Quarantine: true},
		{Name: "peer-down", Quarantine: false},
	}
	req := ScenariosForLane(all, LaneRequired)
	if len(req) != 2 {
		t.Fatalf("required lane has %d scenarios, want 2", len(req))
	}
	for _, s := range req {
		if s.Quarantine {
			t.Errorf("required lane must not include quarantined scenario %q", s.Name)
		}
	}
}

func TestScenariosForLane_QuarantineOnlyQuarantined(t *testing.T) {
	all := []Scenario{
		{Name: "smoke", Quarantine: false},
		{Name: "flaky-thing", Quarantine: true},
	}
	q := ScenariosForLane(all, LaneQuarantine)
	if len(q) != 1 || q[0].Name != "flaky-thing" {
		t.Fatalf("quarantine lane = %+v, want only flaky-thing", q)
	}
}

func TestLaneFromEnv_DefaultsToRequired(t *testing.T) {
	if got := LaneFromEnv(""); got != LaneRequired {
		t.Errorf("empty env => %q, want %q", got, LaneRequired)
	}
	if got := LaneFromEnv("quarantine"); got != LaneQuarantine {
		t.Errorf("'quarantine' => %q, want %q", got, LaneQuarantine)
	}
	if got := LaneFromEnv("required"); got != LaneRequired {
		t.Errorf("'required' => %q, want %q", got, LaneRequired)
	}
}

// The committed Epic-1 spec must have ZERO quarantined scenarios (the quarantine
// lane exists as a mechanism, but nothing should currently be flaky).
func TestCommittedSpecHasNoQuarantinedScenarios(t *testing.T) {
	all, err := LoadAll(scenarioDir(t))
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	q := ScenariosForLane(all, LaneQuarantine)
	if len(q) != 0 {
		t.Errorf("Epic 1 expects zero quarantined scenarios, found %d: %v", len(q), names(q))
	}
	if len(ScenariosForLane(all, LaneRequired)) != len(all) {
		t.Error("all Epic-1 scenarios must be in the required lane")
	}
}

func names(ss []Scenario) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}
