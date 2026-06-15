// Package messaging is Identity's domain facade over the shared bus-side blueprint
// mechanics in libs/go-pitwall/messaging (Story 2.1). The envelope codec, /contract
// validator, reconnect supervisor, consumer Bus, publisher, outbox-relay publish path
// and DLQ/retry/parking topology all live ONCE in the library; this package keeps only
// Identity's own topology (its exchange + DLX + routing keys + consume queue) and
// re-exports the generic types so the consumer and main read idiomatically.
//
// Identity is the first service that is BOTH a consumer and an outbox-backed producer:
// it CONSUMES identity.lookup_requested off the originating service's exchange
// (frontend.events; Q&A Round 30) and PUBLISHES identity.resolved to its OWN
// identity.events exchange via the outbox relay. So this facade re-exports both the
// consumer Bus (DialConsumer) and the confirm Publisher (DialPublisher).
package messaging

import (
	"time"

	libmsg "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// --- shared blueprint mechanics, re-exported from libs/go-pitwall/messaging ---

// Envelope is the standard wire envelope (defined once in the library).
type Envelope = libmsg.Envelope

// IncomingEnvelope is the consume-side view (Data kept raw until the type is known).
type IncomingEnvelope = libmsg.IncomingEnvelope

// HeartbeatData is the control.heartbeat payload (library-defined).
type HeartbeatData = libmsg.HeartbeatData

// Validator validates messages against /contract (validate-on-publish/consume).
type Validator = libmsg.Validator

// Delivery is the broker-agnostic view of a consumed message (unit-testable seam).
type Delivery = libmsg.Delivery

// ConsumerOptions configures the durable queue + DLQ topology this service consumes.
type ConsumerOptions = libmsg.ConsumerOptions

// Bus owns the consumer AMQP connection (own exchange + a consumed queue + DLQ).
type Bus = libmsg.Bus

// Publisher owns the producer AMQP connection (own exchange + confirm channel).
type Publisher = libmsg.Publisher

// ConfirmChannel publishes persistent messages and waits for the broker's confirm ack.
type ConfirmChannel = libmsg.ConfirmChannel

// HeartbeatRoutingKey is the cross-cutting 1 s liveness routing key.
const HeartbeatRoutingKey = libmsg.HeartbeatRoutingKey

// ParkReasonHeader records why a message was parked (observable on the parking queue).
const ParkReasonHeader = libmsg.ParkReasonHeader

// FormatWireTime renders t as a contract-compliant timestamp string.
func FormatWireTime(t time.Time) string { return libmsg.FormatWireTime(t) }

// NewHeartbeatEnvelope builds a fully-populated control.heartbeat envelope.
func NewHeartbeatEnvelope(service, instanceID, correlationID string, now time.Time) Envelope {
	return libmsg.NewHeartbeatEnvelope(service, instanceID, correlationID, now)
}

// DecodeIncoming parses raw bytes into an IncomingEnvelope (no /contract validation).
func DecodeIncoming(payload []byte) (IncomingEnvelope, error) { return libmsg.DecodeIncoming(payload) }

// ResolveContractDir locates the /contract tree (honors CONTRACT_DIR; else walks up).
func ResolveContractDir(explicit string) (string, error) { return libmsg.ResolveContractDir(explicit) }

// NewValidator compiles the envelope + every event data schema under contractDir.
func NewValidator(contractDir string) (*Validator, error) { return libmsg.NewValidator(contractDir) }

// DialConsumer connects, opens a channel and declares Identity's own durable topic
// exchange (for heartbeats); the consumer queue is declared by ConnectAndConsume.
func DialConsumer(uri, ownExchange string) (*Bus, error) { return libmsg.DialConsumerBus(uri, ownExchange) }

// DialPublisher connects and declares Identity's own exchange for the confirm-mode
// outbox relay that publishes identity.resolved.
func DialPublisher(uri, exchange string) (*Publisher, error) { return libmsg.DialPublisher(uri, exchange) }

// RetryQueueName / ParkingQueueName derive the DLQ queue names from the work queue.
func RetryQueueName(workQueue string) string   { return libmsg.RetryQueueName(workQueue) }
func ParkingQueueName(workQueue string) string { return libmsg.ParkingQueueName(workQueue) }

// --- Identity domain topology (exchange + DLX + routing keys + queue) ---

// IdentityExchange is the service's OWN durable topic exchange — it publishes only
// here (identity.resolved + the 1 s heartbeat). [02-message-bus-and-contracts §1]
const IdentityExchange = "identity.events"

// IdentityDLXExchange is the consumer-side dead-letter exchange (AR10: <consumer>.dlx),
// distinct from IdentityExchange. Passed into ConsumerOptions.DLXExchange so the
// library mechanics stay domain-free.
const IdentityDLXExchange = "identity.dlx"

// Routing keys (== envelope `type`, <entity>.<action>). The lookup request is consumed
// off the ORIGINATING service's exchange (frontend.events); the resolved reply is
// published to IdentityExchange. Their /contract data schemas live under
// contract/schemas/frontend/ and contract/schemas/identity/ respectively (Q30.1).
const (
	LookupRequestedRoutingKey = "identity.lookup_requested"
	ResolvedRoutingKey        = "identity.resolved"
)

// LookupRequestedQueue is Identity's durable consumer queue, bound to the originating
// exchange on LookupRequestedRoutingKey.
const LookupRequestedQueue = "identity.lookup-requested"
