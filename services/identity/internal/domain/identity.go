// Package domain holds Identity's pure, broker- and DB-free logic: the resolve-or-mint
// decision and the identity.resolved reply builder. It is the system-of-record rule
// for the canonical masterId (FR1/FR2/NFR15) expressed without any I/O so it stays
// unit-testable. The actual email lookup + unique-constraint single-writer live in the
// persistence Store; the routing key (topology) is passed in by the messaging layer.
package domain

import (
	"strings"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// NormalizeEmail returns the canonical form of the email natural key (Q&A Round 31):
// surrounding whitespace trimmed and the whole address lowercased. Identity is the
// AUTHORITATIVE point of normalization — it applies this before it de-duplicates,
// stores, and echoes the value, so case/whitespace variants of one mailbox
// (Jeremy@X.com, " jeremy@x.com ") resolve to exactly one masterId (AC2/FR1/FR2/NFR15).
// It does not trust producers to send a canonical form. A blank or whitespace-only
// input collapses to "" (the handler parks that as a blank natural key).
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// LookupData is the identity.lookup_requested data payload
// (contract/schemas/frontend/identity.lookup_requested.v1.schema.json).
type LookupData struct {
	RequestID string `json:"requestId"`
	Email     string `json:"email"`
}

// ResolvedData is the identity.resolved data payload
// (contract/schemas/identity/identity.resolved.v1.schema.json). No isNew flag by
// design (FR2) — consumers idempotently upsert on masterId.
type ResolvedData struct {
	RequestID string `json:"requestId"`
	Email     string `json:"email"`
	MasterID  string `json:"masterId"`
}

// LookupRequest is a decoded lookup plus the envelope bits the reply must echo to
// stay correlated: the request envelope id becomes the reply's causationId, and the
// correlationId is copied verbatim across the whole flow.
type LookupRequest struct {
	RequestID     string
	Email         string
	EnvelopeID    string
	CorrelationID string
}

// Resolve decides the masterId for a lookup: an existing non-empty id is reused
// (minted=false); otherwise a fresh id is minted via gen (minted=true). Pure — the
// caller supplies the lookup result and the id generator, so the decision is
// deterministic in tests and carries no DB or randomness of its own. A blank existing
// id (even with found=true) is defensively treated as a miss, never reused.
func Resolve(existing string, found bool, gen func() string) (masterID string, minted bool) {
	if found && existing != "" {
		return existing, false
	}
	return gen(), true
}

// BuildResolved builds the identity.resolved reply for a lookup. It is caused by the
// request (causationId = the request envelope id, never null) and preserves the flow
// correlationId. routingKey is the service's topology constant (passed in so the
// domain stays free of exchange/routing names).
func BuildResolved(routingKey, source, occurredAt string, req LookupRequest, masterID string) messaging.Envelope {
	return messaging.NewCausedEnvelope(
		routingKey,
		source,
		req.CorrelationID,
		req.EnvelopeID,
		occurredAt,
		ResolvedData{RequestID: req.RequestID, Email: req.Email, MasterID: masterID},
	)
}
