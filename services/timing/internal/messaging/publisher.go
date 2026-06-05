package messaging

import (
	"context"
	"errors"

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

// OpenConfirmChannel opens a SECOND channel on the same connection, in publisher
// confirm mode, for the outbox relay (Story 1.4). It is separate from the
// heartbeat channel so the ephemeral fire-and-forget heartbeat path is never
// coupled to confirms. The caller must Close it (before the Publisher) on
// shutdown.
func (p *Publisher) OpenConfirmChannel() (*ConfirmChannel, error) {
	ch, err := p.conn.Channel()
	if err != nil {
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, err
	}
	return &ConfirmChannel{ch: ch, exchange: p.exchange}, nil
}

// ConfirmChannel publishes persistent messages to the owned exchange and waits
// for the broker's publisher-confirm ack. A nil error from PublishConfirmed means
// the broker durably accepted the message — only then may the relay mark the
// outbox row sent (AC1: sent only after ack).
type ConfirmChannel struct {
	ch       *amqp.Channel
	exchange string
}

// PublishConfirmed publishes body to routingKey and blocks until the broker acks
// (or nacks, or ctx expires). A nack or timeout is a publish failure — the row
// stays pending and is retried; it is NEVER reported as a false success.
func (c *ConfirmChannel) PublishConfirmed(ctx context.Context, routingKey string, body []byte) error {
	dc, err := c.ch.PublishWithDeferredConfirmWithContext(ctx, c.exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
	if err != nil {
		return err
	}
	acked, err := dc.WaitContext(ctx)
	if err != nil {
		return err
	}
	if !acked {
		return errors.New("publish nacked by broker")
	}
	return nil
}

// Close closes the confirm channel.
func (c *ConfirmChannel) Close() error {
	if c.ch != nil {
		return c.ch.Close()
	}
	return nil
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
