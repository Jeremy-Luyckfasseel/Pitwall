package messaging

import (
	libmsg "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// The reconnect-aware consumer Bus + DLQ topology + supervisor live once in
// libs/go-pitwall/messaging (Story 2.1). Leaderboard re-exports them so existing call
// sites keep using messaging.Bus / messaging.ConsumerOptions / messaging.Dial and the
// DLQ helpers unchanged. The DLX name itself is Leaderboard's topology (the constant
// below), passed into ConsumerOptions.DLXExchange.

// Bus owns the AMQP connection + channel (own exchange + a consumed queue + DLQ).
type Bus = libmsg.Bus

// Delivery is the broker-agnostic view of a consumed message.
type Delivery = libmsg.Delivery

// ConsumerOptions configures the durable queue + DLQ topology this service consumes.
type ConsumerOptions = libmsg.ConsumerOptions

// Dial connects to the broker, opens a channel and declares the service's own durable
// topic exchange (fail-fast at startup).
func Dial(uri, ownExchange string) (*Bus, error) {
	return libmsg.DialConsumerBus(uri, ownExchange)
}

// LeaderboardDLXExchange is the consumer-side dead-letter exchange (a DIRECT exchange —
// precise key routing), distinct from LeaderboardExchange ("leaderboard.events", where
// heartbeats publish). This is Leaderboard's topology — passed into
// ConsumerOptions.DLXExchange so the library mechanics stay domain-free (AR10:
// `<consumer>.dlx`).
const LeaderboardDLXExchange = "leaderboard.dlx"

// ParkReasonHeader records why a message was parked (observable on the parking queue).
const ParkReasonHeader = libmsg.ParkReasonHeader

// RetryQueueName / ParkingQueueName derive the DLQ queue names from the work queue.
func RetryQueueName(workQueue string) string   { return libmsg.RetryQueueName(workQueue) }
func ParkingQueueName(workQueue string) string { return libmsg.ParkingQueueName(workQueue) }
