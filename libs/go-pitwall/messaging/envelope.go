// Package messaging holds the bus-side blueprint mechanics shared by every Pitwall
// Go service: the standard wire envelope + its codec, the /contract JSON-Schema
// validator wrapper (validate-on-publish / validate-on-consume), and — in later
// extraction steps — the reconnect supervisor, publisher, consumer Bus, outbox
// relay and DLQ/retry/parking helpers. It carries blueprint mechanics ONLY: no
// service exchange names, routing keys, or domain payload shapes (those stay in
// each service, passed in as config).
package messaging

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
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

// HeartbeatRoutingKey is the contract routing key (== envelope `type`) for the
// cross-cutting 1 s liveness signal. It is an <entity>.<action> form (the envelope
// `type` pattern requires the dot) — see Q&A Round 25. Heartbeats are a blueprint
// concern emitted identically by every service, so they live in the shared library.
const HeartbeatRoutingKey = "control.heartbeat"

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

// IncomingEnvelope is the consume-side view of an envelope: identical to Envelope but
// with Data kept as raw JSON so the type-specific payload can be decoded only after
// the envelope's `type` is known (tolerant reader). Generic across all consumers.
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

// DecodeIncoming parses raw bytes into an IncomingEnvelope. It does NOT validate
// against /contract — call the Validator for that first (validate-on-consume). The
// caller then decodes IncomingEnvelope.Data into its own typed domain payload.
func DecodeIncoming(payload []byte) (IncomingEnvelope, error) {
	var env IncomingEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return IncomingEnvelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}

// NewDomainEnvelope fills the standard envelope for a flow-originating domain event:
// a fresh time-ordered UUID v7 id, the given routing key as `type`, the supplied
// correlationId, a null causationId (flow-originating), and the typed data. Services
// build their own typed events on top of this (supplying their routing keys + data).
func NewDomainEnvelope(routingKey, source, correlationID, occurredAt string, data any) Envelope {
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

// NewCausedEnvelope is NewDomainEnvelope for an event caused by another message: it
// stamps causationId with the id of the triggering envelope (never null), preserving
// the correlationId so the whole flow stays linked. Used by reactive handlers (e.g. a
// privacy.erased ack caused by a privacy.erasure_requested).
func NewCausedEnvelope(routingKey, source, correlationID, causationID, occurredAt string, data any) Envelope {
	cid := causationID
	return Envelope{
		ID:              uuid.Must(uuid.NewV7()).String(),
		Type:            routingKey,
		Source:          source,
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      occurredAt,
		CorrelationID:   correlationID,
		CausationID:     &cid,
		Data:            data,
	}
}
