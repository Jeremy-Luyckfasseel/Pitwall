// Package messaging implements the bus-side blueprint blocks the Leaderboard
// service needs: the standard envelope, validate-against-/contract (on CONSUME
// here, the mirror of Timing's validate-on-publish), the service's own durable
// exchange + heartbeat publisher, and a durable consumer queue bound to the
// PRODUCER's exchange (timing.events). Duplicated from services/timing/internal/
// messaging; the libs/go-pitwall extraction is Story 2.1.
package messaging

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LeaderboardExchange is the service's OWN durable topic exchange. A service
// publishes only to its own exchange (blueprint §Messaging) — here, just its
// 1 s control.heartbeat. Domain consumption happens FROM the producer's
// exchange (see Consumer / TimingExchange), never from this one.
const LeaderboardExchange = "leaderboard.events"

// HeartbeatRoutingKey is the contract routing key for the cross-cutting 1 s
// liveness signal (<entity>.<action>; Q&A Round 25).
const HeartbeatRoutingKey = "control.heartbeat"

// LapRecordedRoutingKey is the producer event this service consumes. Its
// /contract data schema lives at contract/schemas/timing/lap.recorded.v1.
const LapRecordedRoutingKey = "lap.recorded"

// SessionStartedRoutingKey / SessionEndedRoutingKey are the session lifecycle
// events this service consumes (Story 1.8: auto-reset + active/finished status).
// Their /contract data schemas live at contract/schemas/timing/session.*.v1.
const (
	SessionStartedRoutingKey = "session.started"
	SessionEndedRoutingKey   = "session.ended"
)

// wireTimeLayout renders timestamps in the canonical wire format: RFC3339 UTC,
// exactly 3-digit milliseconds, literal 'Z' (AR9).
const wireTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// FormatWireTime renders t as a contract-compliant timestamp string.
func FormatWireTime(t time.Time) string {
	return t.UTC().Format(wireTimeLayout)
}

// Envelope is the standard message envelope (all 9 fields), camelCase wire names.
// CausationID is a pointer so a flow-originating event serializes as `null`.
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

// NewHeartbeatEnvelope builds a fully-populated control.heartbeat envelope. The
// heartbeat is flow-originating, so causationId is null and correlationId is the
// service's lifecycle id. occurredAt and data.at are stamped from now.
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

// IncomingEnvelope is the consume-side view of an envelope: identical to Envelope
// but with Data kept as raw JSON so the type-specific payload can be decoded only
// after the envelope's `type` is known (tolerant reader).
type IncomingEnvelope struct {
	ID              string          `json:"id"`
	Type            string          `json:"type"`
	Source          string          `json:"source"`
	SchemaVersion   int             `json:"schemaVersion"`
	EnvelopeVersion int             `json:"envelopeVersion"`
	OccurredAt      string          `json:"occurredAt"`
	CorrelationID   string          `json:"correlationId"`
	CausationID     *string         `json:"causationId"`
	Data            json.RawMessage `json:"data"`
}

// LapRecordedData is the timing/lap.recorded.v1 payload. The schema is a tolerant
// reader (additionalProperties:true); this struct reads only the fields the
// Leaderboard needs (transponderId is consumed but unused here).
type LapRecordedData struct {
	MasterID      string  `json:"masterId"`
	SessionID     string  `json:"sessionId"`
	LapNumber     int     `json:"lapNumber"`
	LapTimeMs     int64   `json:"lapTimeMs"`
	At            string  `json:"at"`
	TransponderID *string `json:"transponderId"`
}

// DecodeIncoming parses raw bytes into an IncomingEnvelope. It does NOT validate
// against /contract — call the Validator for that first (validate-on-consume).
func DecodeIncoming(payload []byte) (IncomingEnvelope, error) {
	var env IncomingEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return IncomingEnvelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}

// DecodeLapRecorded extracts the lap.recorded data payload from an incoming
// envelope. The caller has already validated the bytes against /contract.
func DecodeLapRecorded(env IncomingEnvelope) (LapRecordedData, error) {
	var d LapRecordedData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return LapRecordedData{}, fmt.Errorf("decode lap.recorded data: %w", err)
	}
	return d, nil
}

// SessionStartedData is the timing/session.started.v1 payload (tolerant reader:
// only the fields the board needs).
type SessionStartedData struct {
	SessionID string `json:"sessionId"`
	StartedAt string `json:"startedAt"`
}

// SessionEndedData is the timing/session.ended.v1 payload. It deliberately does
// NOT decode summary[]: the per-item shape is intentionally unpinned in v1
// (confirm-at-build when Driver/Mailing consume it — Epic 3/10), and the board's
// finished standings come from its own projection.
type SessionEndedData struct {
	SessionID string `json:"sessionId"`
	EndedAt   string `json:"endedAt"`
}

// DecodeSessionStarted extracts the session.started data payload from an
// incoming envelope. The caller has already validated the bytes against /contract.
func DecodeSessionStarted(env IncomingEnvelope) (SessionStartedData, error) {
	var d SessionStartedData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return SessionStartedData{}, fmt.Errorf("decode session.started data: %w", err)
	}
	return d, nil
}

// DecodeSessionEnded extracts the session.ended data payload from an incoming
// envelope. The caller has already validated the bytes against /contract.
func DecodeSessionEnded(env IncomingEnvelope) (SessionEndedData, error) {
	var d SessionEndedData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return SessionEndedData{}, fmt.Errorf("decode session.ended data: %w", err)
	}
	return d, nil
}
