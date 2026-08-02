package messaging

import (
	"encoding/json"
	"reflect"
	"testing"

	timingcodegen "github.com/Jeremy-Luyckfasseel/Pitwall/contract/codegen/go/timing"
)

// These tests prove Timing's hand-written wire structs are a faithful match for the
// /contract-generated DTOs (Story 3.1, AC2 — "codegen is a faithful drop-in, not a
// parallel definition"). They do NOT literally replace the hand-written struct types
// at every call site: LapRecordedData.LapTimeMs is int64 (matching the domain's lap-
// time arithmetic throughout timing/internal/domain and the simulator), while the
// generated LapRecordedV1SchemaJson.LapTimeMs is plain int (go-jsonschema's default
// for an unbounded JSON integer) — a real, intentional type difference at the Go level
// that would ripple through dozens of call sites for zero behavioral gain. Per
// architecture's own "codegen owns the wire boundary only; domain models are
// hand-written and idiomatic, mapped to/from the generated DTOs at the edge", the
// mapping happens here: a structural equivalence proof that both types marshal the
// SAME logical event to byte-identical wire JSON. A drift between the schema and the
// hand-written struct (missing field, renamed field, wrong nullability) fails this
// test immediately, without risking the domain-wide blast radius of a literal type
// swap.
func marshalToMap(t *testing.T, v any) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %T back to map: %v", v, err)
	}
	return m
}

func TestLapRecordedData_MatchesGeneratedDTO(t *testing.T) {
	tp := "TP-00421"
	handWritten := LapRecordedData{
		MasterID:      "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21",
		SessionID:     "session-2026-05-31-evening-heat-3",
		LapNumber:     7,
		LapTimeMs:     42318,
		At:            "2026-05-31T14:03:21.500Z",
		TransponderID: &tp,
	}
	generated := timingcodegen.LapRecordedV1SchemaJson{
		MasterID:      handWritten.MasterID,
		SessionID:     handWritten.SessionID,
		LapNumber:     handWritten.LapNumber,
		LapTimeMs:     int(handWritten.LapTimeMs),
		At:            handWritten.At,
		TransponderID: &tp,
	}

	got, want := marshalToMap(t, handWritten), marshalToMap(t, generated)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LapRecordedData diverges from the generated DTO:\n  hand-written: %#v\n  generated:    %#v", got, want)
	}

	// The nullable field must always be a present key, never omitted, on the QR path too.
	handWritten.TransponderID = nil
	generated.TransponderID = nil
	got, want = marshalToMap(t, handWritten), marshalToMap(t, generated)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LapRecordedData (nil transponderId) diverges from the generated DTO:\n  hand-written: %#v\n  generated:    %#v", got, want)
	}
	if _, present := got["transponderId"]; !present {
		t.Error("transponderId must be a present key (null), never omitted")
	}
}

func TestSessionStartedData_MatchesGeneratedDTO(t *testing.T) {
	handWritten := SessionStartedData{SessionID: "session-42", StartedAt: "2026-05-31T14:00:00.000Z"}
	generated := timingcodegen.SessionStartedV1SchemaJson{SessionID: handWritten.SessionID, StartedAt: handWritten.StartedAt}

	got, want := marshalToMap(t, handWritten), marshalToMap(t, generated)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SessionStartedData diverges from the generated DTO:\n  hand-written: %#v\n  generated:    %#v", got, want)
	}
}

func TestSessionEndedData_MatchesGeneratedDTO(t *testing.T) {
	handWritten := SessionEndedData{
		SessionID: "session-42",
		EndedAt:   "2026-05-31T15:00:00.000Z",
		Summary: []SessionSummaryRow{
			{MasterID: "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", BestLapMs: 41000, LapCount: 10},
		},
	}
	generated := timingcodegen.SessionEndedV1SchemaJson{
		SessionID: handWritten.SessionID,
		EndedAt:   handWritten.EndedAt,
		Summary: []timingcodegen.SessionEndedV1SchemaJsonSummaryElem{
			{"masterId": handWritten.Summary[0].MasterID, "bestLapMs": float64(handWritten.Summary[0].BestLapMs), "lapCount": float64(handWritten.Summary[0].LapCount)},
		},
	}

	got, want := marshalToMap(t, handWritten), marshalToMap(t, generated)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SessionEndedData diverges from the generated DTO:\n  hand-written: %#v\n  generated:    %#v", got, want)
	}
}

func TestCheckedInData_MatchesGeneratedDTO(t *testing.T) {
	tp := "TP-00421"
	handWritten := CheckedInData{
		MasterID:      "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21",
		At:            "2026-05-31T14:00:00.000Z",
		CheckInMethod: CheckInMethodTransponder,
		TransponderID: &tp,
	}
	generated := timingcodegen.DriverCheckedInV1SchemaJson{
		MasterID:      handWritten.MasterID,
		At:            handWritten.At,
		CheckInMethod: timingcodegen.DriverCheckedInV1SchemaJsonCheckInMethod(handWritten.CheckInMethod),
		TransponderID: &tp,
	}

	got, want := marshalToMap(t, handWritten), marshalToMap(t, generated)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CheckedInData diverges from the generated DTO:\n  hand-written: %#v\n  generated:    %#v", got, want)
	}
}
