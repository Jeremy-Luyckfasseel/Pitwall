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
