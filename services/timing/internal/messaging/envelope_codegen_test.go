package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	timingcodegen "github.com/Jeremy-Luyckfasseel/Pitwall/contract/codegen/go/timing"
)

// findRepoRoot/loadFixtureData mirror services/leaderboard's identical helpers — kept
// duplicated rather than shared (no common test-support package exists between
// services, and one doesn't seem worth introducing for two small helpers).
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
	bestLapMs := int(handWritten.Summary[0].BestLapMs)
	lapCount := handWritten.Summary[0].LapCount
	generated := timingcodegen.SessionEndedV1SchemaJson{
		SessionID: handWritten.SessionID,
		EndedAt:   handWritten.EndedAt,
		Summary: []timingcodegen.SessionEndedV1SchemaJsonSummaryElem{
			{MasterID: handWritten.Summary[0].MasterID, BestLapMs: &bestLapMs, LapCount: &lapCount},
		},
	}

	got, want := marshalToMap(t, handWritten), marshalToMap(t, generated)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SessionEndedData diverges from the generated DTO:\n  hand-written: %#v\n  generated:    %#v", got, want)
	}
}

func TestPersonalRecordBrokenData_MatchesGeneratedDTO(t *testing.T) {
	previous := int64(42318)
	handWritten := PersonalRecordBrokenData{
		MasterID:   "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21",
		SessionID:  "session-2026-05-31-evening-heat-3",
		LapTimeMs:  41980,
		PreviousMs: &previous,
	}
	prev := int(previous)
	generated := timingcodegen.PersonalRecordBrokenV1SchemaJson{
		MasterID:   handWritten.MasterID,
		SessionID:  handWritten.SessionID,
		LapTimeMs:  int(handWritten.LapTimeMs),
		PreviousMs: &prev,
	}

	// With previousMs PRESENT both types marshal to identical wire JSON.
	got, want := marshalToMap(t, handWritten), marshalToMap(t, generated)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PersonalRecordBrokenData diverges from the generated DTO:\n  hand-written: %#v\n  generated:    %#v", got, want)
	}

	// FIRST-PR case (Q37.2): the hand-written struct OMITS previousMs entirely (its
	// `,omitempty` tag) — the schema-correct representation, since previousMs is
	// `type: integer` (not nullable) and simply absent on a first PR. The generated DTO
	// (--disable-omitempty) instead emits `previousMs: null`, which the schema REJECTS,
	// so the two intentionally diverge here and only the hand-written struct is used to
	// publish. Assert the hand-written struct omits the key.
	handWritten.PreviousMs = nil
	got = marshalToMap(t, handWritten)
	if _, present := got["previousMs"]; present {
		t.Error("first-PR personal_record.broken must OMIT previousMs (not emit null)")
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

// The tests above only prove ENCODE-side equivalence (both types marshal hand-picked
// literal field values to identical JSON) — they would miss a drift that only breaks
// unmarshal, e.g. a schema field renamed such that one type silently fails to populate
// it from real wire JSON while the other still does. The tests below close that gap by
// decoding a real, committed contract/examples fixture into both types and comparing
// the result (mirrors services/leaderboard's and services/identity's equivalence
// tests, Story 3.1 code review).

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

func TestDecodeSessionEnded_MatchesGeneratedDTO(t *testing.T) {
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
	if len(handWritten.Summary) != len(generated.Summary) {
		t.Fatalf("summary length diverges: hand-written=%d generated=%d", len(handWritten.Summary), len(generated.Summary))
	}
	for i, row := range handWritten.Summary {
		gen := generated.Summary[i]
		if gen.BestLapMs == nil || gen.LapCount == nil {
			t.Fatalf("summary[%d] generated DTO has nil bestLapMs/lapCount: %+v", i, gen)
		}
		if row.MasterID != gen.MasterID ||
			row.BestLapMs != int64(*gen.BestLapMs) ||
			row.LapCount != *gen.LapCount {
			t.Errorf("summary[%d] diverges: hand-written=%+v generated=%+v", i, row, gen)
		}
	}
}

func TestDecodeCheckedIn_MatchesGeneratedDTO(t *testing.T) {
	data := loadFixtureData(t, filepath.Join("timing", "driver.checked_in.v1.example.json"))

	var handWritten CheckedInData
	if err := json.Unmarshal(data, &handWritten); err != nil {
		t.Fatalf("decode into CheckedInData: %v", err)
	}
	var generated timingcodegen.DriverCheckedInV1SchemaJson
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode into generated DTO: %v", err)
	}

	if handWritten.MasterID != generated.MasterID ||
		handWritten.At != generated.At ||
		string(handWritten.CheckInMethod) != string(generated.CheckInMethod) {
		t.Errorf("CheckedInData diverges from the generated DTO:\n  hand-written: %+v\n  generated:    %+v", handWritten, generated)
	}
	if (handWritten.TransponderID == nil) != (generated.TransponderID == nil) {
		t.Fatalf("transponderId nullability diverges: hand-written=%v generated=%v", handWritten.TransponderID, generated.TransponderID)
	}
	if handWritten.TransponderID != nil && *handWritten.TransponderID != *generated.TransponderID {
		t.Errorf("transponderId value diverges: hand-written=%q generated=%q", *handWritten.TransponderID, *generated.TransponderID)
	}
}
