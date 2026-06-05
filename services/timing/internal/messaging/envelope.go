// Package messaging implements the bus-side blueprint blocks the Timing skeleton
// needs in Story 1.3: the standard envelope, validate-against-/contract, the
// service's own durable exchange, and a publisher. Consumer/inbox/outbox land in
// later stories (1.4/1.7/1.9).
package messaging

import (
	"time"

	"github.com/google/uuid"
)

// Exchange is the service's own durable topic exchange. A service publishes only
// to its own exchange (blueprint §Messaging; 02-message-bus-and-contracts §1).
const TimingExchange = "timing.events"

// HeartbeatRoutingKey is the contract routing key for the cross-cutting 1 s
// liveness signal. It is an <entity>.<action> form (the envelope `type` pattern
// requires the dot) — see Q&A Round 25.
const HeartbeatRoutingKey = "control.heartbeat"

// Routing keys for Timing's core domain events (== envelope `type`; the
// <entity>.<action> form). Their /contract data schemas live under
// contract/schemas/timing/ (Story 1.2).
const (
	LapRecordedRoutingKey    = "lap.recorded"
	SessionStartedRoutingKey = "session.started"
	SessionEndedRoutingKey   = "session.ended"
)

// wireTimeLayout renders timestamps in the canonical wire format: RFC3339 UTC,
// exactly 3-digit milliseconds, literal 'Z' (AR9). Go's default RFC3339 emits a
// numeric offset and variable fractional digits, so the format is explicit and
// applied to a UTC time.
const wireTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// FormatWireTime renders t as a contract-compliant timestamp string.
func FormatWireTime(t time.Time) string {
	return t.UTC().Format(wireTimeLayout)
}

// Envelope is the standard message envelope (all 9 fields). Field names are the
// camelCase wire names. CausationID is a pointer so a flow-originating event
// serializes as `null` (the key must be present, never omitted).
type Envelope struct {
	ID              string  `json:"id"`
	Type            string  `json:"type"`
	Source          string  `json:"source"`
	SchemaVersion   int     `json:"schemaVersion"`
	EnvelopeVersion int     `json:"envelopeVersion"`
	OccurredAt      string  `json:"occurredAt"`
	CorrelationID   string  `json:"correlationId"`
	CausationID     *string `json:"causationId"`
	Data            any     `json:"data"`
}

// HeartbeatData is the control.heartbeat payload (matches
// contract/schemas/control/control.heartbeat.v1.schema.json).
type HeartbeatData struct {
	Service    string `json:"service"`
	At         string `json:"at"`
	InstanceID string `json:"instanceId"`
}

// NewHeartbeatEnvelope builds a fully-populated control.heartbeat envelope.
// The heartbeat is flow-originating, so causationId is null and correlationId is
// the service's lifecycle id. occurredAt and data.at are stamped from now.
func NewHeartbeatEnvelope(service, instanceID, correlationID string, now time.Time) Envelope {
	ts := FormatWireTime(now)
	return Envelope{
		ID:              uuid.Must(uuid.NewV7()).String(),
		Type:            HeartbeatRoutingKey,
		Source:          service,
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      ts,
		CorrelationID:   correlationID,
		CausationID:     nil,
		Data: HeartbeatData{
			Service:    service,
			At:         ts,
			InstanceID: instanceID,
		},
	}
}

// LapRecordedData is the timing/lap.recorded.v1 payload. TransponderID is a
// pointer so an absent transponder (QR-equivalent driver) serializes as null —
// the field is always present (no omitempty on a meaningful wire field, AR9).
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

// SessionSummaryRow is one per-driver row in a session.ended summary. The v1
// schema leaves the item shape tolerant (no Epic-1 consumer reads it); this is
// the minimal useful shape (matches the committed example) and may be pinned
// later when Driver/Mailing consume it (Epic 3/10).
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

// NewLapRecordedEnvelope builds a fully-populated lap.recorded envelope. occurredAt
// and data.at are both stamped from at (the crossing time). Simulator events are
// flow-originating, so causationId is null and correlationId is the session's id.
func NewLapRecordedEnvelope(source, correlationID, masterID, sessionID string, lapNumber int, lapTimeMs int64, transponderID *string, at time.Time) Envelope {
	ts := FormatWireTime(at)
	return domainEnvelope(LapRecordedRoutingKey, source, correlationID, ts, LapRecordedData{
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
	return domainEnvelope(SessionStartedRoutingKey, source, correlationID, ts, SessionStartedData{
		SessionID: sessionID,
		StartedAt: ts,
	})
}

// NewSessionEndedEnvelope builds a session.ended envelope; occurredAt == endedAt.
func NewSessionEndedEnvelope(source, correlationID, sessionID string, endedAt time.Time, summary []SessionSummaryRow) Envelope {
	ts := FormatWireTime(endedAt)
	return domainEnvelope(SessionEndedRoutingKey, source, correlationID, ts, SessionEndedData{
		SessionID: sessionID,
		EndedAt:   ts,
		Summary:   summary,
	})
}

// domainEnvelope fills the standard envelope for a Timing domain event: a fresh
// time-ordered UUID v7 id, the given routing key as type, the per-session
// correlationId, a null causationId (flow-originating), and the typed data.
func domainEnvelope(routingKey, source, correlationID, occurredAt string, data any) Envelope {
	return Envelope{
		ID:              uuid.Must(uuid.NewV7()).String(),
		Type:            routingKey,
		Source:          source,
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      occurredAt,
		CorrelationID:   correlationID,
		CausationID:     nil,
		Data:            data,
	}
}
