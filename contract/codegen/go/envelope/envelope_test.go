package envelope

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// findRepoRoot walks up from the current package directory until it finds
// contract/examples, mirroring libs/go-pitwall/messaging.ResolveContractDir's
// approach (this module has no dependency on that lib, so it is duplicated here in
// miniature rather than imported — mechanics only, zero deps).
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

// TestEnvelope_RoundTripsEveryCommittedExample proves the generated EnvelopeSchemaJson
// is a faithful drop-in for the envelope shape: every *.example.json fixture under
// /contract/examples unmarshals into it and re-marshals byte-for-byte equivalent
// (compared structurally, since map key order is not guaranteed) — no field lost, no
// field renamed, no type coerced away (Story 3.1, AC1/AC2 — codegen is a faithful
// drop-in, not a parallel definition).
func TestEnvelope_RoundTripsEveryCommittedExample(t *testing.T) {
	root := findRepoRoot(t)
	examplesDir := filepath.Join(root, "contract", "examples")

	var fixtures []string
	err := filepath.WalkDir(examplesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".example.json") {
			fixtures = append(fixtures, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk examples dir: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no example fixtures found — examples dir resolution is broken")
	}

	for _, path := range fixtures {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			var env EnvelopeSchemaJson
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("unmarshal into EnvelopeSchemaJson: %v", err)
			}

			remarshaled, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}

			var original, roundTripped map[string]interface{}
			if err := json.Unmarshal(raw, &original); err != nil {
				t.Fatalf("unmarshal original for comparison: %v", err)
			}
			if err := json.Unmarshal(remarshaled, &roundTripped); err != nil {
				t.Fatalf("unmarshal round-tripped for comparison: %v", err)
			}

			if !reflect.DeepEqual(original, roundTripped) {
				t.Errorf("round-trip mismatch for %s:\n  original:      %#v\n  round-tripped: %#v", path, original, roundTripped)
			}
		})
	}
}
