// Package messaging is Timing's domain facade over the shared bus-side blueprint
// mechanics in libs/go-pitwall/messaging. The generic envelope codec + /contract
// validator now live ONCE in the library (Story 2.1); this package keeps only
// Timing's own topology (its exchange + routing keys) and typed domain events,
// re-exporting the generic types so existing call sites keep using messaging.Envelope
// etc. unchanged.
package messaging

import (
	"time"

	libmsg "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// --- shared blueprint mechanics, re-exported from libs/go-pitwall/messaging ---

// Envelope is the standard wire envelope (defined once in the library).
type Envelope = libmsg.Envelope

// HeartbeatData is the control.heartbeat payload (library-defined).
type HeartbeatData = libmsg.HeartbeatData

// HeartbeatRoutingKey is the cross-cutting 1 s liveness routing key.
const HeartbeatRoutingKey = libmsg.HeartbeatRoutingKey

// FormatWireTime renders t as a contract-compliant timestamp string.
func FormatWireTime(t time.Time) string { return libmsg.FormatWireTime(t) }

// NewHeartbeatEnvelope builds a fully-populated control.heartbeat envelope.
func NewHeartbeatEnvelope(service, instanceID, correlationID string, now time.Time) Envelope {
	return libmsg.NewHeartbeatEnvelope(service, instanceID, correlationID, now)
}

// --- Timing domain (topology + typed events) ---

// TimingExchange is the service's own durable topic exchange. A service publishes
// only to its own exchange (blueprint §Messaging; 02-message-bus-and-contracts §1).
const TimingExchange = "timing.events"

// Routing keys for Timing's core domain events (== envelope `type`; the
// <entity>.<action> form). Their /contract data schemas live under
// contract/schemas/timing/ (Story 1.2).
const (
	LapRecordedRoutingKey          = "lap.recorded"
	SessionStartedRoutingKey       = "session.started"
	SessionEndedRoutingKey         = "session.ended"
	DriverCheckedInRoutingKey      = "driver.checked_in"
	PersonalRecordBrokenRoutingKey = "personal_record.broken"
)

// Check-in methods for driver.checked_in.checkInMethod (pinned by the /contract enum).
const (
	CheckInMethodQR          = "qr"
	CheckInMethodTransponder = "transponder"
)

// LapRecordedData is the timing/lap.recorded.v1 payload. TransponderID is a pointer
// so an absent transponder (QR-equivalent driver) serializes as null — the field is
// always present (no omitempty on a meaningful wire field, AR9).
type LapRecordedData struct {
	MasterID      string  `json:"masterId"`
	SessionID     string  `json:"sessionId"`
	LapNumber     int     `json:"lapNumber"`
	LapTimeMs     int64   `json:"lapTimeMs"`
	At            string  `json:"at"`
	TransponderID *string `json:"transponderId"`
}

// SessionStartedData is the timing/session.started.v1 payload.
type SessionStartedData struct {
	SessionID string `json:"sessionId"`
	StartedAt string `json:"startedAt"`
}

// SessionSummaryRow is one per-driver row in a session.ended summary. The v1 schema
// leaves the item shape tolerant (no Epic-1 consumer reads it); this is the minimal
// useful shape (matches the committed example) and may be pinned later when
// Driver/Mailing consume it (Epic 3/10).
type SessionSummaryRow struct {
	MasterID  string `json:"masterId"`
	BestLapMs int64  `json:"bestLapMs"`
	LapCount  int    `json:"lapCount"`
}

// SessionEndedData is the timing/session.ended.v1 payload.
type SessionEndedData struct {
	SessionID string              `json:"sessionId"`
	EndedAt   string              `json:"endedAt"`
	Summary   []SessionSummaryRow `json:"summary"`
}

// LookupRequestedData is the frontend/identity.lookup_requested.v1 payload (the
// register-first lookup the simulator sends, impersonating the Frontend registration
// producer). Identity replies identity.resolved correlated by requestId.
type LookupRequestedData struct {
	RequestID string `json:"requestId"`
	Email     string `json:"email"`
}

// NewLookupRequestedEnvelope builds an identity.lookup_requested envelope. source is
// "frontend" (the Frontend stand-in — Q&A Round 30/32), correlationId starts the flow,
// causationId is null (flow-originating).
func NewLookupRequestedEnvelope(source, correlationID, requestID, email string, at time.Time) Envelope {
	return libmsg.NewDomainEnvelope(LookupRequestedRoutingKey, source, correlationID, FormatWireTime(at), LookupRequestedData{
		RequestID: requestID,
		Email:     email,
	})
}

// CheckedInData is the timing/driver.checked_in.v1 payload. TransponderID is a pointer
// so a QR driver (no transponder) serializes as null — the field is always present (no
// omitempty on a meaningful wire field, AR9; same rule as LapRecordedData).
type CheckedInData struct {
	MasterID      string  `json:"masterId"`
	At            string  `json:"at"`
	CheckInMethod string  `json:"checkInMethod"`
	TransponderID *string `json:"transponderId"`
}

// NewCheckedInEnvelope builds a fully-populated driver.checked_in envelope. occurredAt
// and data.at are both stamped from at (the gate-scan time). Simulator/gate events are
// flow-originating, so causationId is null and correlationId is the session's id.
func NewCheckedInEnvelope(source, correlationID, masterID, checkInMethod string, transponderID *string, at time.Time) Envelope {
	ts := FormatWireTime(at)
	return libmsg.NewDomainEnvelope(DriverCheckedInRoutingKey, source, correlationID, ts, CheckedInData{
		MasterID:      masterID,
		At:            ts,
		CheckInMethod: checkInMethod,
		TransponderID: transponderID,
	})
}

// NewLapRecordedEnvelope builds a fully-populated lap.recorded envelope. occurredAt
// and data.at are both stamped from at (the crossing time). Simulator events are
// flow-originating, so causationId is null and correlationId is the session's id.
func NewLapRecordedEnvelope(source, correlationID, masterID, sessionID string, lapNumber int, lapTimeMs int64, transponderID *string, at time.Time) Envelope {
	ts := FormatWireTime(at)
	return libmsg.NewDomainEnvelope(LapRecordedRoutingKey, source, correlationID, ts, LapRecordedData{
		MasterID:      masterID,
		SessionID:     sessionID,
		LapNumber:     lapNumber,
		LapTimeMs:     lapTimeMs,
		At:            ts,
		TransponderID: transponderID,
	})
}

// NewSessionStartedEnvelope builds a session.started envelope; occurredAt == startedAt.
func NewSessionStartedEnvelope(source, correlationID, sessionID string, startedAt time.Time) Envelope {
	ts := FormatWireTime(startedAt)
	return libmsg.NewDomainEnvelope(SessionStartedRoutingKey, source, correlationID, ts, SessionStartedData{
		SessionID: sessionID,
		StartedAt: ts,
	})
}

// PersonalRecordBrokenData is the timing/personal_record.broken.v1 payload (Story 3.4,
// FR37). PreviousMs is a pointer with `,omitempty`: on a FIRST-ever PR there is nothing
// to beat, so the key is OMITTED entirely (Q37.2). This deliberately differs from the
// generated DTO, whose --disable-omitempty pointer would serialize `previousMs: null` —
// and null is INVALID against the schema (previousMs is `type: integer`, not nullable),
// so the hand-written struct is the schema-correct wire representation. LapTimeMs is
// int64 to match the domain's lap-time arithmetic (mapped to the generated int at the
// codegen equivalence boundary, envelope_codegen_test.go).
type PersonalRecordBrokenData struct {
	MasterID   string `json:"masterId"`
	SessionID  string `json:"sessionId"`
	LapTimeMs  int64  `json:"lapTimeMs"`
	PreviousMs *int64 `json:"previousMs,omitempty"`
}

// NewPersonalRecordBrokenEnvelope builds a personal_record.broken envelope. It is
// flow-originating (like every simulator-produced event: lap.recorded, session.ended,
// …) — causationId null, correlationId = the session's id — because it is produced in
// the same live lap flow as its sibling lap.recorded, with no prior consumed envelope to
// cause it. previousMs nil (a first PR) omits the field.
func NewPersonalRecordBrokenEnvelope(source, correlationID, masterID, sessionID string, lapTimeMs int64, previousMs *int64, at time.Time) Envelope {
	return libmsg.NewDomainEnvelope(PersonalRecordBrokenRoutingKey, source, correlationID, FormatWireTime(at), PersonalRecordBrokenData{
		MasterID:   masterID,
		SessionID:  sessionID,
		LapTimeMs:  lapTimeMs,
		PreviousMs: previousMs,
	})
}

// NewSessionEndedEnvelope builds a session.ended envelope; occurredAt == endedAt.
func NewSessionEndedEnvelope(source, correlationID, sessionID string, endedAt time.Time, summary []SessionSummaryRow) Envelope {
	ts := FormatWireTime(endedAt)
	return libmsg.NewDomainEnvelope(SessionEndedRoutingKey, source, correlationID, ts, SessionEndedData{
		SessionID: sessionID,
		EndedAt:   ts,
		Summary:   summary,
	})
}
