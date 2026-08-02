package erasure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	frontendcodegen "github.com/Jeremy-Luyckfasseel/Pitwall/contract/codegen/go/frontend"
	privacycodegen "github.com/Jeremy-Luyckfasseel/Pitwall/contract/codegen/go/privacy"
)

// These tests prove the erasure scaffold's internal wire structs are a faithful match
// for the /contract-generated DTOs (Story 3.1, AC2). requestData is a deliberate
// tolerant partial read (only requestId+masterId — the scaffold needs nothing else);
// erasedData is full-fidelity (all 5 required privacy.erased fields).

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

func TestRequestData_MatchesGeneratedDTO_ForTheFieldsItDecodes(t *testing.T) {
	data := loadFixtureData(t, filepath.Join("frontend", "privacy.erasure_requested.v1.example.json"))

	var handWritten requestData
	if err := json.Unmarshal(data, &handWritten); err != nil {
		t.Fatalf("decode into requestData: %v", err)
	}
	var generated frontendcodegen.PrivacyErasureRequestedV1SchemaJson
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode into generated DTO: %v", err)
	}

	if handWritten.RequestID != generated.RequestID || handWritten.MasterID != generated.MasterID {
		t.Errorf("requestData diverges from the generated DTO:\n  hand-written: %+v\n  generated:    %+v", handWritten, generated)
	}
}

func TestErasedData_MatchesGeneratedDTO(t *testing.T) {
	data := loadFixtureData(t, filepath.Join("privacy", "privacy.erased.v1.example.json"))

	var handWritten erasedData
	if err := json.Unmarshal(data, &handWritten); err != nil {
		t.Fatalf("decode into erasedData: %v", err)
	}
	var generated privacycodegen.PrivacyErasedV1SchemaJson
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode into generated DTO: %v", err)
	}

	if handWritten.RequestID != generated.RequestID ||
		handWritten.MasterID != generated.MasterID ||
		handWritten.Service != generated.Service ||
		handWritten.Mode != string(generated.Mode) ||
		handWritten.At != generated.At {
		t.Errorf("erasedData diverges from the generated DTO:\n  hand-written: %+v\n  generated:    %+v", handWritten, generated)
	}
}
