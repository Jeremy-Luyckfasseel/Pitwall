package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	timingcodegen "github.com/Jeremy-Luyckfasseel/Pitwall/contract/codegen/go/timing"
)

// These tests prove Leaderboard's consume-side decoders are a faithful (if
// intentionally partial) match for the /contract-generated DTOs (Story 3.1, AC2).
// LapRecordedData decodes every field the schema defines; SessionEndedData
// deliberately omits `summary` (the board's finished standings come from its own
// projection, not the tolerant/unpinned summary rows — see the doc comment on
// SessionEndedData) — so its equivalence proof is "every field it DOES decode matches
// the generated DTO", not full-struct equality.

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

func loadFixtureData(t *testing.T, relPath string) json.RawMessage {
	t.Helper()
	root := findRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "contract", "examples", relPath))
	if err != nil {
		t.Fatalf("read fixture %s: %v", relPath, err)
	}
	var full struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatalf("unmarshal envelope wrapper: %v", err)
	}
	return full.Data
}

func TestDecodeLapRecorded_MatchesGeneratedDTO(t *testing.T) {
	data := loadFixtureData(t, filepath.Join("timing", "lap.recorded.v1.example.json"))

	var handWritten LapRecordedData
	if err := json.Unmarshal(data, &handWritten); err != nil {
		t.Fatalf("decode into LapRecordedData: %v", err)
	}
	var generated timingcodegen.LapRecordedV1SchemaJson
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode into generated DTO: %v", err)
	}

	if handWritten.MasterID != generated.MasterID ||
		handWritten.SessionID != generated.SessionID ||
		handWritten.LapNumber != generated.LapNumber ||
		int(handWritten.LapTimeMs) != generated.LapTimeMs ||
		handWritten.At != generated.At {
		t.Errorf("LapRecordedData diverges from the generated DTO:\n  hand-written: %+v\n  generated:    %+v", handWritten, generated)
	}
	if (handWritten.TransponderID == nil) != (generated.TransponderID == nil) {
		t.Fatalf("transponderId nullability diverges: hand-written=%v generated=%v", handWritten.TransponderID, generated.TransponderID)
	}
	if handWritten.TransponderID != nil && *handWritten.TransponderID != *generated.TransponderID {
		t.Errorf("transponderId value diverges: hand-written=%q generated=%q", *handWritten.TransponderID, *generated.TransponderID)
	}
}

func TestDecodeSessionStarted_MatchesGeneratedDTO(t *testing.T) {
	data := loadFixtureData(t, filepath.Join("timing", "session.started.v1.example.json"))

	var handWritten SessionStartedData
	if err := json.Unmarshal(data, &handWritten); err != nil {
		t.Fatalf("decode into SessionStartedData: %v", err)
	}
	var generated timingcodegen.SessionStartedV1SchemaJson
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode into generated DTO: %v", err)
	}

	if handWritten.SessionID != generated.SessionID || handWritten.StartedAt != generated.StartedAt {
		t.Errorf("SessionStartedData diverges from the generated DTO:\n  hand-written: %+v\n  generated:    %+v", handWritten, generated)
	}
}

func TestDecodeSessionEnded_MatchesGeneratedDTO_ForTheFieldsItDecodes(t *testing.T) {
	data := loadFixtureData(t, filepath.Join("timing", "session.ended.v1.example.json"))

	var handWritten SessionEndedData
	if err := json.Unmarshal(data, &handWritten); err != nil {
		t.Fatalf("decode into SessionEndedData: %v", err)
	}
	var generated timingcodegen.SessionEndedV1SchemaJson
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode into generated DTO: %v", err)
	}

	if handWritten.SessionID != generated.SessionID || handWritten.EndedAt != generated.EndedAt {
		t.Errorf("SessionEndedData diverges from the generated DTO:\n  hand-written: %+v\n  generated:    %+v", handWritten, generated)
	}
	// Deliberately NOT comparing Summary — SessionEndedData intentionally does not
	// decode it (see the type's doc comment); the generated DTO's presence of
	// Summary here just proves the fixture itself carries it (the source of truth
	// for "the field exists on the wire, this decoder chooses to ignore it").
	if len(generated.Summary) == 0 {
		t.Fatal("fixture unexpectedly carries an empty summary — test fixture assumption broken")
	}
}
