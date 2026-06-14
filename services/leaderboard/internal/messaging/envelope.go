// Package messaging is Leaderboard's domain facade over the shared bus-side
// blueprint mechanics in libs/go-pitwall/messaging. The generic envelope codec,
// the consume-side IncomingEnvelope/DecodeIncoming, and the /contract validator now
// live ONCE in the library (Story 2.1); this package keeps only Leaderboard's own
// topology (its exchange + the producer routing keys it consumes) and the typed
// domain decoders, re-exporting the generic types so existing call sites keep using
// messaging.Envelope etc. unchanged.
package messaging

import (
	"encoding/json"
	"fmt"
	"time"

	libmsg "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// --- shared blueprint mechanics, re-exported from libs/go-pitwall/messaging ---

// Envelope is the standard wire envelope (defined once in the library).
type Envelope = libmsg.Envelope

// IncomingEnvelope is the consume-side view (Data kept as raw JSON for tolerant reading).
type IncomingEnvelope = libmsg.IncomingEnvelope

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

// DecodeIncoming parses raw bytes into an IncomingEnvelope (no /contract validation;
// call the Validator first).
func DecodeIncoming(payload []byte) (IncomingEnvelope, error) { return libmsg.DecodeIncoming(payload) }

// --- Leaderboard domain (topology + typed decoders) ---

// LeaderboardExchange is the service's OWN durable topic exchange. A service
// publishes only to its own exchange (blueprint §Messaging) — here, just its 1 s
// control.heartbeat. Domain consumption happens FROM the producer's exchange (the
// timing.events exchange), never from this one.
const LeaderboardExchange = "leaderboard.events"

// LapRecordedRoutingKey is the producer event this service consumes. Its /contract
// data schema lives at contract/schemas/timing/lap.recorded.v1.
const LapRecordedRoutingKey = "lap.recorded"

// SessionStartedRoutingKey / SessionEndedRoutingKey are the session lifecycle events
// this service consumes (Story 1.8: auto-reset + active/finished status). Their
// /contract data schemas live at contract/schemas/timing/session.*.v1.
const (
	SessionStartedRoutingKey = "session.started"
	SessionEndedRoutingKey   = "session.ended"
)

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

// DecodeLapRecorded extracts the lap.recorded data payload from an incoming envelope.
// The caller has already validated the bytes against /contract.
func DecodeLapRecorded(env IncomingEnvelope) (LapRecordedData, error) {
	var d LapRecordedData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return LapRecordedData{}, fmt.Errorf("decode lap.recorded data: %w", err)
	}
	return d, nil
}

// SessionStartedData is the timing/session.started.v1 payload (tolerant reader: only
// the fields the board needs).
type SessionStartedData struct {
	SessionID string `json:"sessionId"`
	StartedAt string `json:"startedAt"`
}

// SessionEndedData is the timing/session.ended.v1 payload. It deliberately does NOT
// decode summary[]: the per-item shape is intentionally unpinned in v1 (confirm-at-
// build when Driver/Mailing consume it — Epic 3/10), and the board's finished
// standings come from its own projection.
type SessionEndedData struct {
	SessionID string `json:"sessionId"`
	EndedAt   string `json:"endedAt"`
}

// DecodeSessionStarted extracts the session.started data payload from an incoming
// envelope. The caller has already validated the bytes against /contract.
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
