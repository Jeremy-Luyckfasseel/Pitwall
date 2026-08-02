package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	frontendcodegen "github.com/Jeremy-Luyckfasseel/Pitwall/contract/codegen/go/frontend"
	identitycodegen "github.com/Jeremy-Luyckfasseel/Pitwall/contract/codegen/go/identity"
)

// These tests prove Identity's hand-written wire structs are a faithful match for the
// /contract-generated DTOs (Story 3.1, AC2). Both LookupData and ResolvedData are flat
// (no int64/time.Time fields), so a direct decode-and-compare against a committed
// fixture is sufficient — no adapter mapping needed at this boundary.

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

func TestLookupData_MatchesGeneratedDTO(t *testing.T) {
	data := loadFixtureData(t, filepath.Join("frontend", "identity.lookup_requested.v1.example.json"))

	var handWritten LookupData
	if err := json.Unmarshal(data, &handWritten); err != nil {
		t.Fatalf("decode into LookupData: %v", err)
	}
	var generated frontendcodegen.IdentityLookupRequestedV1SchemaJson
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode into generated DTO: %v", err)
	}

	if handWritten.RequestID != generated.RequestID || handWritten.Email != generated.Email {
		t.Errorf("LookupData diverges from the generated DTO:\n  hand-written: %+v\n  generated:    %+v", handWritten, generated)
	}
}

func TestResolvedData_MatchesGeneratedDTO(t *testing.T) {
	data := loadFixtureData(t, filepath.Join("identity", "identity.resolved.v1.example.json"))

	var handWritten ResolvedData
	if err := json.Unmarshal(data, &handWritten); err != nil {
		t.Fatalf("decode into ResolvedData: %v", err)
	}
	var generated identitycodegen.IdentityResolvedV1SchemaJson
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode into generated DTO: %v", err)
	}

	if handWritten.RequestID != generated.RequestID ||
		handWritten.Email != generated.Email ||
		handWritten.MasterID != generated.MasterID {
		t.Errorf("ResolvedData diverges from the generated DTO:\n  hand-written: %+v\n  generated:    %+v", handWritten, generated)
	}
}
