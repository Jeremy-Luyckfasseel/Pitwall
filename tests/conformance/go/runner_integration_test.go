//go:build integration

package conformance

import (
	"os"
	"testing"
)

// scenarioRunners maps a scenario name (from scenarios/*.yaml) to its Go
// implementation. The YAML spec is the language-neutral source of truth; this
// map is the thin Go runner that executes it (AR16). A future
// tests/conformance/<lang>/ runner maps the SAME names to its own implementations.
var scenarioRunners = map[string]func(t *testing.T, sc Scenario){
	"smoke":             runSmoke,
	"checkin-chain":     runCheckinChain,
	"peer-down":         runPeerDown,
	"inbox-dup":         runInboxDup,
	"publish-redeliver": runPublishRedeliver,
	"crash-after-ack":   runCrashAfterAck,
}

// TestConformance is the single entry point: it loads the whole scenario spec,
// selects the lane (CONFORMANCE_LANE; default "required" = the merge gate), and
// runs each scenario in that lane as a subtest. Quarantined scenarios run only in
// the quarantine lane — they are routed, NEVER @skip-ped (AR16). Run a single
// scenario locally with e.g. `go test -tags=integration -run TestConformance/smoke`.
func TestConformance(t *testing.T) {
	all, err := LoadAll(scenarioDir(t))
	if err != nil {
		t.Fatalf("load scenario spec: %v", err)
	}
	lane := LaneFromEnv(os.Getenv(LaneEnvVar))
	scenarios := ScenariosForLane(all, lane)
	t.Logf("conformance lane=%q: running %d/%d scenarios", lane, len(scenarios), len(all))

	for _, sc := range scenarios {
		run, ok := scenarioRunners[sc.Name]
		if !ok {
			t.Errorf("scenario %q has no Go runner (add it to scenarioRunners)", sc.Name)
			continue
		}
		t.Run(sc.Name, func(t *testing.T) { run(t, sc) })
	}
}
