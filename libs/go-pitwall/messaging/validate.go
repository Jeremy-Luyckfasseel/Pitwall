package messaging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validator validates messages against the /contract JSON Schemas (blueprint
// §Messaging: validate every message in and out). It compiles the envelope schema,
// the heartbeat data schema, and an index of every event data schema keyed by its
// wire `type`, once at startup. A message that fails is logged + quarantined/rejected
// by the caller, never published nor applied (CLAUDE.md rule 5).
type Validator struct {
	envelope  *jsonschema.Schema
	heartbeat *jsonschema.Schema
	data      map[string]*jsonschema.Schema // wire type -> data-payload schema
}

// ResolveContractDir returns the directory holding the /contract tree. It honors
// CONTRACT_DIR (set explicitly in the container, where /contract is COPYed in) and
// otherwise walks up from the working directory to find it (dev/test).
func ResolveContractDir(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		cand := filepath.Join(dir, "contract", "schemas", "envelope.schema.json")
		if _, err := os.Stat(cand); err == nil {
			return filepath.Join(dir, "contract"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate the /contract directory (set CONTRACT_DIR)")
		}
		dir = parent
	}
}

// NewValidator compiles the envelope schema, the heartbeat data schema, and every
// event data schema under contractDir. contractDir is the path to the /contract tree
// (see ResolveContractDir).
func NewValidator(contractDir string) (*Validator, error) {
	envSchema, err := compile(filepath.Join(contractDir, "schemas", "envelope.schema.json"))
	if err != nil {
		return nil, fmt.Errorf("compile envelope schema: %w", err)
	}
	hbSchema, err := compile(filepath.Join(contractDir, "schemas", "control", "control.heartbeat.v1.schema.json"))
	if err != nil {
		return nil, fmt.Errorf("compile heartbeat schema: %w", err)
	}
	data, err := indexDataSchemas(filepath.Join(contractDir, "schemas"))
	if err != nil {
		return nil, fmt.Errorf("index data schemas: %w", err)
	}
	return &Validator{envelope: envSchema, heartbeat: hbSchema, data: data}, nil
}

// schemaVersionSuffix matches the trailing version segment of a schema filename
// stem, e.g. "lap.recorded.v1" -> type "lap.recorded".
var schemaVersionSuffix = regexp.MustCompile(`\.v\d+$`)

// indexDataSchemas compiles every event data schema under schemasDir, keyed by its
// wire `type` (filename stem minus the trailing `.vN`). The envelope meta-schema is
// skipped (it is not a data payload).
func indexDataSchemas(schemasDir string) (map[string]*jsonschema.Schema, error) {
	out := map[string]*jsonschema.Schema{}
	err := filepath.WalkDir(schemasDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".schema.json") || d.Name() == "envelope.schema.json" {
			return nil
		}
		stem := strings.TrimSuffix(d.Name(), ".schema.json")
		typ := schemaVersionSuffix.ReplaceAllString(stem, "")
		s, cerr := compile(path)
		if cerr != nil {
			return fmt.Errorf("compile %s: %w", path, cerr)
		}
		out[typ] = s
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ValidateHeartbeat validates the full envelope against the envelope schema and its
// data against the heartbeat data schema. A non-nil error means do not publish.
func (v *Validator) ValidateHeartbeat(env Envelope) error {
	inst, err := toInstance(env)
	if err != nil {
		return err
	}
	if err := v.envelope.Validate(inst); err != nil {
		return fmt.Errorf("envelope: %w", err)
	}
	m, ok := inst.(map[string]any)
	if !ok {
		return fmt.Errorf("envelope did not marshal to a JSON object")
	}
	if err := v.heartbeat.Validate(m["data"]); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	return nil
}

// ValidateEnvelopeBytes validates a marshalled message: the full envelope against the
// envelope schema, then its data against the schema registered for the envelope's
// `type`. It fails closed — an unknown type (no registered data schema) is an error,
// because a service must never publish nor apply an event it cannot validate. A
// non-nil error means: do not publish/process; quarantine or reject the message.
func (v *Validator) ValidateEnvelopeBytes(payload []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("payload is not valid JSON: %w", err)
	}
	if err := v.envelope.Validate(inst); err != nil {
		return fmt.Errorf("envelope: %w", err)
	}
	m, ok := inst.(map[string]any)
	if !ok {
		return fmt.Errorf("envelope did not marshal to a JSON object")
	}
	typ, _ := m["type"].(string)
	ds, ok := v.data[typ]
	if !ok {
		return fmt.Errorf("no /contract data schema for type %q", typ)
	}
	if err := ds.Validate(m["data"]); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	return nil
}

func compile(path string) (*jsonschema.Schema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	const id = "schema.json"
	if err := c.AddResource(id, doc); err != nil {
		return nil, err
	}
	return c.Compile(id)
}

// toInstance converts a Go value into the native JSON representation
// (map[string]any / float64 / ...) that the validator expects.
func toInstance(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(b))
}
