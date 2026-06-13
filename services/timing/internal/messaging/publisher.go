package messaging

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Publisher owns the AMQP connection + channels and the service's own durable
// topic exchange. It publishes only to that exchange (blueprint §Messaging).
//
// Story 1.10 makes it reconnect-aware: amqp091-go does NOT auto-recover a dropped
// connection, so the Publisher's own supervisor (ConnectAndServe) re-dials with
// capped-exponential backoff and re-declares the exchange + reopens the confirm
// channel after a bus restart. The connection/channels live behind a mutex; the
// publish methods read the CURRENT channel, so the relay's and heartbeat's
// captured closures keep working across a reconnect (a publish made while
// disconnected returns an error — never a false success).
type Publisher struct {
	uri      string
	exchange string

	mu      sync.RWMutex
	conn    *amqp.Connection
	ch      *amqp.Channel // heartbeat (fire-and-forget)
	confirm *amqp.Channel // confirm-mode channel for the outbox relay
	closed  chan struct{} // closes when the CURRENT connection dies
}

// Dial connects to the broker, opens the channels and declares the durable topic
// exchange (fail-fast at startup). The caller must Close the publisher on
// shutdown. To survive a mid-session bus kill, call ConnectAndServe afterwards.
func Dial(uri, exchange string) (*Publisher, error) {
	p := &Publisher{uri: uri, exchange: exchange}
	if _, err := p.establish(context.Background()); err != nil {
		return nil, err
	}
	return p, nil
}

// establish opens one connection generation: dial, open the heartbeat + confirm
// channels, declare the durable exchange, swap them in under the lock, and return
// a channel that closes when THIS connection dies. Re-run on every reconnect, so
// the exchange is re-declared on a fresh broker process.
func (p *Publisher) establish(_ context.Context) (<-chan struct{}, error) {
	conn, err := amqp.Dial(p.uri)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	// durable, non-auto-delete, non-internal — survives broker restart.
	if err := ch.ExchangeDeclare(p.exchange, "topic", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	confirm, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := confirm.Confirm(false); err != nil {
		_ = conn.Close()
		return nil, err
	}

	closed := make(chan struct{})
	notify := conn.NotifyClose(make(chan *amqp.Error, 1))
	go func() {
		<-notify // fires on close (nil error on a graceful Close)
		close(closed)
	}()

	p.mu.Lock()
	p.conn, p.ch, p.confirm, p.closed = conn, ch, confirm, closed
	p.mu.Unlock()
	return closed, nil
}

// ConnectAndServe supervises the connection established by Dial: it adopts the
// live connection, then on every drop re-dials (capped-exponential backoff),
// reopens the channels and re-declares the exchange — reporting connected<->lost
// through onState (nil-safe). It returns immediately (the supervisor runs in the
// background) and stops when ctx is cancelled. Dial must have succeeded first
// (fail-fast); ConnectAndServe never re-dials the first generation.
func (p *Publisher) ConnectAndServe(ctx context.Context, log *slog.Logger, onState func(connected bool)) error {
	p.mu.RLock()
	adopted := p.closed
	p.mu.RUnlock()
	if adopted == nil {
		return errors.New("ConnectAndServe called before a successful Dial")
	}
	connect := func(ctx context.Context) (<-chan struct{}, error) {
		if adopted != nil {
			c := adopted
			adopted = nil
			return c, nil // adopt the connection Dial already established
		}
		return p.establish(ctx)
	}
	sup := newSupervisor(connect, defaultBackoff(), onState, log)
	go sup.Run(ctx)
	return nil
}

// Publish sends a persistent JSON message to the owned exchange with the given
// routing key, using the CURRENT channel. A publish while disconnected returns an
// error (the heartbeat then logs+skips, leaving the liveness file stale — the
// honest bus-down signal).
func (p *Publisher) Publish(ctx context.Context, routingKey string, body []byte) error {
	p.mu.RLock()
	ch := p.ch
	p.mu.RUnlock()
	if ch == nil {
		return errors.New("publish: no broker channel")
	}
	return ch.PublishWithContext(ctx, p.exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
}

// PublishConfirmed publishes body to routingKey on the internal confirm channel
// and blocks until the broker acks (or nacks/ctx-expires/connection-drops). A
// nack, timeout, or dropped connection is a publish failure — the outbox row
// stays pending and is retried; it is NEVER reported as a false success. This is
// the production relay's Publisher (survives reconnect, unlike a ConfirmChannel
// bound to a fixed channel).
func (p *Publisher) PublishConfirmed(ctx context.Context, routingKey string, body []byte) error {
	p.mu.RLock()
	ch := p.confirm
	p.mu.RUnlock()
	if ch == nil {
		return errors.New("publish: no confirm channel")
	}
	dc, err := ch.PublishWithDeferredConfirmWithContext(ctx, p.exchange, routingKey, false, false, amqp.Publishing{
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

// OpenConfirmChannel opens a SEPARATE confirm-mode channel on the current
// connection. Retained for tests that drive a relay directly; production uses
// PublishConfirmed (reconnect-aware). The caller must Close it.
func (p *Publisher) OpenConfirmChannel() (*ConfirmChannel, error) {
	p.mu.RLock()
	conn := p.conn
	p.mu.RUnlock()
	if conn == nil {
		return nil, errors.New("open confirm channel: no connection")
	}
	ch, err := conn.Channel()
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

// Close shuts the channels then the connection. Safe to call once at shutdown.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var firstErr error
	if p.confirm != nil {
		if err := p.confirm.Close(); err != nil {
			firstErr = err
		}
	}
	if p.ch != nil {
		if err := p.ch.Close(); err != nil && firstErr == nil {
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
