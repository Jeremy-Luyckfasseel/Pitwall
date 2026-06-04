package messaging

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Publisher owns the AMQP connection + channel and the service's own durable
// topic exchange. It publishes only to that exchange (blueprint §Messaging).
// Reconnect-after-bus-kill resilience is intentionally out of scope here — that
// is Story 1.10; this skeleton connects once and publishes.
type Publisher struct {
	conn     *amqp.Connection
	ch       *amqp.Channel
	exchange string
}

// Dial connects to the broker, opens a channel and declares the durable topic
// exchange. The caller must Close the publisher on shutdown.
func Dial(uri, exchange string) (*Publisher, error) {
	conn, err := amqp.Dial(uri)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	// durable, non-auto-delete, non-internal — survives broker restart.
	if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, err
	}
	return &Publisher{conn: conn, ch: ch, exchange: exchange}, nil
}

// Publish sends a persistent JSON message to the owned exchange with the given
// routing key. It blocks until the publish call returns (synchronous, so a
// graceful shutdown between ticks never interrupts an in-flight publish).
func (p *Publisher) Publish(ctx context.Context, routingKey string, body []byte) error {
	return p.ch.PublishWithContext(ctx, p.exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
}

// Close shuts the channel then the connection, in that order.
func (p *Publisher) Close() error {
	var firstErr error
	if p.ch != nil {
		if err := p.ch.Close(); err != nil {
			firstErr = err
		}
	}
	if p.conn != nil {
		if err := p.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
