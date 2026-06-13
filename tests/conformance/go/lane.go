package conformance

import "strings"

// Conformance lanes (AR16). The REQUIRED lane is the merge gate — it runs the
// non-quarantined scenarios. The QUARANTINE lane runs scenarios flagged flaky in
// the spec; it is non-blocking but STILL RUNS them — a flaky scenario is moved to
// this lane, NEVER @skip-ped. Which lane a CI job runs is selected by the
// CONFORMANCE_LANE env var.
const (
	LaneRequired   = "required"
	LaneQuarantine = "quarantine"
	// LaneEnvVar selects the lane for a run (default: required).
	LaneEnvVar = "CONFORMANCE_LANE"
)

// LaneFromEnv maps a raw env value to a lane, defaulting to the required lane.
func LaneFromEnv(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), LaneQuarantine) {
		return LaneQuarantine
	}
	return LaneRequired
}

// ScenariosForLane returns the subset of scenarios that run in the given lane:
// the required lane gets the non-quarantined scenarios, the quarantine lane gets
// the quarantined ones. No scenario is ever dropped from BOTH lanes.
func ScenariosForLane(all []Scenario, lane string) []Scenario {
	out := make([]Scenario, 0, len(all))
	for _, s := range all {
		if lane == LaneQuarantine && s.Quarantine {
			out = append(out, s)
		}
		if lane != LaneQuarantine && !s.Quarantine {
			out = append(out, s)
		}
	}
	return out
}
