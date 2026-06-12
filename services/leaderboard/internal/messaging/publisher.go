package messaging

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Bus owns the AMQP connection + channel. It declares the service's OWN durable
// topic exchange (for the heartbeat it publishes) and, separately, a durable
// queue bound to a PRODUCER's exchange (for the domain events it consumes).
// Reconnect-after-bus-kill resilience is out of scope here — that is Story 1.10;
// this skeleton connects once.
type Bus struct {
	conn     *amqp.Connection
	ch       *amqp.Channel
	exchange string // the service's own exchange (heartbeat publishes here)
	dlx      string // the consumer-side dead-letter exchange (set by DeclareDLQTopology)
}

// Dial connects to the broker, opens a channel and declares the service's own
// durable topic exchange. The caller must Close the bus on shutdown.
func Dial(uri, ownExchange string) (*Bus, error) {
	conn, err := amqp.Dial(uri)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := ch.ExchangeDeclare(ownExchange, "topic", true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, err
	}
	return &Bus{conn: conn, ch: ch, exchange: ownExchange}, nil
}

// Publish sends a persistent JSON message to the service's OWN exchange with the
// given routing key (used for the 1 s heartbeat). Synchronous, so a graceful
// shutdown between ticks never interrupts an in-flight publish.
func (b *Bus) Publish(ctx context.Context, routingKey string, body []byte) error {
	return b.ch.PublishWithContext(ctx, b.exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
}

// Close shuts the channel then the connection, in that order.
func (b *Bus) Close() error {
	var firstErr error
	if b.ch != nil {
		if err := b.ch.Close(); err != nil {
			firstErr = err
		}
	}
	if b.conn != nil {
		if err := b.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
