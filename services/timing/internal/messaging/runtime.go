package messaging

import (
	libmsg "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// The reconnect-aware publisher runtime lives once in libs/go-pitwall/messaging
// (Story 2.1). Timing re-exports it so existing call sites keep using
// messaging.Publisher / messaging.Dial unchanged. The supervisor + reconnect logic
// are internal to the library.

// Publisher owns the AMQP connection + channels and the service's own durable exchange.
type Publisher = libmsg.Publisher

// ConfirmChannel publishes persistent messages and waits for the broker's confirm ack.
type ConfirmChannel = libmsg.ConfirmChannel

// Dial connects to the broker, opens the channels and declares the service's durable
// topic exchange (fail-fast at startup).
func Dial(uri, exchange string) (*Publisher, error) {
	return libmsg.DialPublisher(uri, exchange)
}
