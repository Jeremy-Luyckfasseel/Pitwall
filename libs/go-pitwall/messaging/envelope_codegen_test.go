package messaging

import (
	"encoding/json"
	"reflect"
	"testing"

	controlcodegen "github.com/Jeremy-Luyckfasseel/Pitwall/contract/codegen/go/control"
)

// TestHeartbeatData_MatchesGeneratedDTO proves the cross-cutting HeartbeatData struct
// (emitted identically by every service) is a faithful match for the /contract-
// generated control.heartbeat DTO (Story 3.1, AC2).
func TestHeartbeatData_MatchesGeneratedDTO(t *testing.T) {
	handWritten := HeartbeatData{Service: "timing", At: "2026-06-02T14:03:21.512Z", InstanceID: "018f9e2a-instance"}
	generated := controlcodegen.ControlHeartbeatV1SchemaJson{
		Service:    handWritten.Service,
		At:         handWritten.At,
		InstanceID: handWritten.InstanceID,
	}

	hb, err := json.Marshal(handWritten)
	if err != nil {
		t.Fatalf("marshal hand-written: %v", err)
	}
	gb, err := json.Marshal(generated)
	if err != nil {
		t.Fatalf("marshal generated: %v", err)
	}
	var hm, gm map[string]interface{}
	if err := json.Unmarshal(hb, &hm); err != nil {
		t.Fatalf("unmarshal hand-written: %v", err)
	}
	if err := json.Unmarshal(gb, &gm); err != nil {
		t.Fatalf("unmarshal generated: %v", err)
	}
	if !reflect.DeepEqual(hm, gm) {
		t.Errorf("HeartbeatData diverges from the generated DTO:\n  hand-written: %#v\n  generated:    %#v", hm, gm)
	}
}
