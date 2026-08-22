package messaging

import (
	libmsg "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// The reconnect-aware publisher runtime lives once in libs/go-pitwall/messaging
// (Story 2.1). Timing re-exports it so existing call sites keep using
// messaging.Publisher / messaging.Dial unchanged. The supervisor + reconnect logic
// are internal to the library.
//
// Story 2.3 makes Timing dual-role: in addition to its outbox-backed publisher
// (timing.events), it CONSUMES identity.resolved off identity.events (Timing's first
// inbound consumer) and — as the Frontend stand-in for the dev simulator — PUBLISHES
// identity.lookup_requested to frontend.events (Q&A Round 30 / Round 32). So the facade
// now also re-exports the consumer Bus, the consume-side envelope types and the DLQ
// helpers.

// Publisher owns the AMQP connection + channels and the service's own durable exchange.
type Publisher = libmsg.Publisher

// ConfirmChannel publishes persistent messages and waits for the broker's confirm ack.
type ConfirmChannel = libmsg.ConfirmChannel

// IncomingEnvelope is the consume-side view (Data kept raw until the type is known).
type IncomingEnvelope = libmsg.IncomingEnvelope

// Delivery is the broker-agnostic view of a consumed message (unit-testable seam).
type Delivery = libmsg.Delivery

// ConsumerOptions configures the durable queue + DLQ topology this service consumes.
type ConsumerOptions = libmsg.ConsumerOptions

// Bus owns the consumer AMQP connection (own exchange + a consumed queue + DLQ).
type Bus = libmsg.Bus

// ParkReasonHeader records why a message was parked (observable on the parking queue).
const ParkReasonHeader = libmsg.ParkReasonHeader

// Dial connects to the broker, opens the channels and declares the service's durable
// topic exchange (fail-fast at startup).
func Dial(uri, exchange string) (*Publisher, error) {
	return libmsg.DialPublisher(uri, exchange)
}

// DialConsumer connects, opens a channel and declares Timing's own durable topic
// exchange (for heartbeats); the consumer queue + DLQ are declared by ConnectAndConsume.
func DialConsumer(uri, ownExchange string) (*Bus, error) {
	return libmsg.DialConsumerBus(uri, ownExchange)
}

// DecodeIncoming parses raw bytes into an IncomingEnvelope (no /contract validation).
func DecodeIncoming(payload []byte) (IncomingEnvelope, error) { return libmsg.DecodeIncoming(payload) }

// RetryQueueName / ParkingQueueName derive the DLQ queue names from the work queue.
func RetryQueueName(workQueue string) string   { return libmsg.RetryQueueName(workQueue) }
func ParkingQueueName(workQueue string) string { return libmsg.ParkingQueueName(workQueue) }

// --- check-in / identity topology (Story 2.3) ---

// TimingDLXExchange is Timing's consumer-side dead-letter exchange (AR10: <consumer>.dlx),
// distinct from TimingExchange. Passed into ConsumerOptions.DLXExchange.
const TimingDLXExchange = "timing.dlx"

// IdentityEventsExchange is Identity's own exchange; Timing binds its consumer queue here
// to receive identity.resolved replies.
const IdentityEventsExchange = "identity.events"

// FrontendEventsExchange is the originating registration surface's exchange. The
// simulator publishes identity.lookup_requested here (source "frontend"), impersonating
// the registration producer (Q&A Round 30 / Q30.1) — Identity binds and replies.
const FrontendEventsExchange = "frontend.events"

// Routing keys for the register-first request/reply (== envelope `type`).
const (
	LookupRequestedRoutingKey  = "identity.lookup_requested"
	IdentityResolvedRoutingKey = "identity.resolved"
)

// IdentityResolvedQueue is Timing's durable consumer queue, bound to identity.events on
// IdentityResolvedRoutingKey.
const IdentityResolvedQueue = "timing.identity-resolved"

// --- PR refresh topology (Story 3.4) ---

// DriverEventsExchange is Driver's own exchange; Timing binds a SECOND consumer queue
// here to receive driver.pr_updated and refresh its local all-time-PR copy (FR37). The
// Go consumer runtime is single-binding-per-queue, so this is a distinct queue from the
// identity.resolved one (both reuse TimingDLXExchange for their DLX).
const DriverEventsExchange = "driver.events"

// DriverPRUpdatedRoutingKey is the confirmed-canonical-PR event (== envelope `type`).
const DriverPRUpdatedRoutingKey = "driver.pr_updated"

// DriverPRUpdatedQueue is Timing's durable consumer queue, bound to driver.events on
// DriverPRUpdatedRoutingKey.
const DriverPRUpdatedQueue = "timing.driver-pr-updated"
