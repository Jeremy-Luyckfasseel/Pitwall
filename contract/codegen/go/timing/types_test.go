package timing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// findRepoRoot walks up from the current package directory until it finds
// contract/examples (mirrors envelope package's helper; this module has no shared
// internal package to hang it off, mechanics only, zero deps).
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "contract", "examples")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root (contract/examples) from %s", dir)
		}
		dir = parent
	}
}

// roundTripData decodes the `data` object of a full envelope fixture into dst,
// re-marshals it, and asserts structural equality against the original `data` object
// — proving the generated per-event struct (not just the generic envelope wrapper) is
// a faithful drop-in for that event's specific fields (Story 3.1, AC1/AC2).
func roundTripData(t *testing.T, fixturePath string, dst any) {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var full struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatalf("unmarshal envelope wrapper: %v", err)
	}
	if err := json.Unmarshal(full.Data, dst); err != nil {
		t.Fatalf("unmarshal data into %T: %v", dst, err)
	}
	remarshaled, err := json.Marshal(dst)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}

	var original, roundTripped map[string]interface{}
	if err := json.Unmarshal(full.Data, &original); err != nil {
		t.Fatalf("unmarshal original data for comparison: %v", err)
	}
	if err := json.Unmarshal(remarshaled, &roundTripped); err != nil {
		t.Fatalf("unmarshal round-tripped data for comparison: %v", err)
	}
	if !reflect.DeepEqual(original, roundTripped) {
		t.Errorf("round-trip mismatch for %s:\n  original:      %#v\n  round-tripped: %#v", fixturePath, original, roundTripped)
	}
}

func TestLapRecordedV1_RoundTripsCommittedExample(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "contract", "examples", "timing", "lap.recorded.v1.example.json")
	var d LapRecordedV1SchemaJson
	roundTripData(t, path, &d)

	if d.MasterID == "" || d.SessionID == "" || d.LapNumber == 0 || d.LapTimeMs == 0 || d.At == "" {
		t.Errorf("expected all required fields populated, got %+v", d)
	}
	if d.TransponderID == nil || *d.TransponderID != "TP-00421" {
		t.Errorf("expected transponderId TP-00421, got %v", d.TransponderID)
	}
}

func TestDriverCheckedInV1_RoundTripsCommittedExample(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "contract", "examples", "timing", "driver.checked_in.v1.example.json")
	var d DriverCheckedInV1SchemaJson
	roundTripData(t, path, &d)

	if d.MasterID == "" || d.At == "" || d.CheckInMethod == "" {
		t.Errorf("expected all required fields populated, got %+v", d)
	}
}

func TestSessionStartedV1_RoundTripsCommittedExample(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "contract", "examples", "timing", "session.started.v1.example.json")
	var d SessionStartedV1SchemaJson
	roundTripData(t, path, &d)

	if d.SessionID == "" || d.StartedAt == "" {
		t.Errorf("expected all required fields populated, got %+v", d)
	}
}

func TestSessionEndedV1_RoundTripsCommittedExample(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "contract", "examples", "timing", "session.ended.v1.example.json")
	var d SessionEndedV1SchemaJson
	roundTripData(t, path, &d)

	if d.SessionID == "" || d.EndedAt == "" || len(d.Summary) == 0 {
		t.Errorf("expected all required fields populated, got %+v", d)
	}
}
