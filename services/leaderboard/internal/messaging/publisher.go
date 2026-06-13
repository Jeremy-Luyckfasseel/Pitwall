package messaging

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// errChannelGone is returned when a publish/declare is attempted while the broker
// connection is down (the supervisor is mid-reconnect). Callers treat it as a
// transient failure (retry / log+skip), never a false success.
var errChannelGone = errors.New("messaging: no broker channel (disconnected)")

// Bus owns the AMQP connection + channel. It declares the service's OWN durable
// topic exchange (for the heartbeat it publishes) and, separately, a durable
// queue bound to a PRODUCER's exchange (for the domain events it consumes).
//
// Story 1.10 makes it reconnect-aware: amqp091-go does NOT auto-recover a dropped
// connection, so the Bus's supervisor (ConnectAndConsume) re-dials with
// capped-exponential backoff, re-declares the exchange + DLQ topology and
// re-subscribes after a bus restart — pumping deliveries into a STABLE channel so
// the consumer Handler never needs restarting. The connection/channel live behind
// a mutex; every publish/declare reads the CURRENT channel, and the
// connection-state callback drives the served bundle's stale flag.
type Bus struct {
	uri      string
	exchange string // the service's own exchange (heartbeat publishes here)
	dlx      string // the consumer-side dead-letter exchange (set by DeclareDLQTopology)

	mu     sync.RWMutex
	conn   *amqp.Connection
	ch     *amqp.Channel
	closed chan struct{} // closes when the CURRENT connection dies

	out chan Delivery // stable deliveries stream handed to the consumer Handler
}

// Dial connects to the broker, opens a channel and declares the service's own
// durable topic exchange (fail-fast at startup). The caller must Close the bus on
// shutdown. To survive a mid-session bus kill, call ConnectAndConsume afterwards.
func Dial(uri, ownExchange string) (*Bus, error) {
	b := &Bus{uri: uri, exchange: ownExchange}
	if _, err := b.establish(); err != nil {
		return nil, err
	}
	return b, nil
}

// establish opens one connection generation: dial, open the channel, declare the
// durable own exchange, swap them in under the lock, and return a channel that
// closes when THIS connection dies. Re-run on every reconnect.
func (b *Bus) establish() (<-chan struct{}, error) {
	conn, err := amqp.Dial(b.uri)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := ch.ExchangeDeclare(b.exchange, "topic", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, err
	}

	closed := make(chan struct{})
	notify := conn.NotifyClose(make(chan *amqp.Error, 1))
	go func() {
		<-notify // fires on close (nil error on a graceful Close)
		close(closed)
	}()

	b.mu.Lock()
	b.conn, b.ch, b.closed = conn, ch, closed
	b.mu.Unlock()
	return closed, nil
}

func (b *Bus) curCh() *amqp.Channel {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ch
}

func (b *Bus) curConn() *amqp.Connection {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.conn
}

// ConnectAndConsume declares the DLQ topology, starts consuming, and supervises
// the connection across bus restarts — re-declaring + re-subscribing on every
// reconnect and pumping deliveries into a STABLE channel (returned) so the
// consumer Handler keeps running across reconnects. It reports connected<->lost
// through onState (the stale flag). The first generation was established by Dial
// (fail-fast); ConnectAndConsume declares+subscribes on it synchronously, then
// supervises in the background until ctx is cancelled. The returned channel is
// never closed by a reconnect — only the Handler's own ctx.Done stops it.
func (b *Bus) ConnectAndConsume(ctx context.Context, opts ConsumerOptions, log *slog.Logger, onState func(connected bool)) (<-chan Delivery, error) {
	b.mu.RLock()
	adopted := b.closed
	b.mu.RUnlock()
	if adopted == nil {
		return nil, errors.New("ConnectAndConsume called before a successful Dial")
	}
	b.out = make(chan Delivery)

	// subscribe (re)declares the topology and starts a pump for the current
	// generation. Run after every (re)connect.
	subscribe := func() error {
		if err := b.DeclareDLQTopology(opts); err != nil {
			return err
		}
		raw, err := b.consumeRaw(opts.QueueName)
		if err != nil {
			return err
		}
		go b.pump(ctx, raw)
		return nil
	}

	// Synchronous first subscribe on the adopted connection (fail-fast).
	if err := subscribe(); err != nil {
		return nil, err
	}

	connect := func(_ context.Context) (<-chan struct{}, error) {
		if adopted != nil {
			c := adopted
			adopted = nil
			return c, nil // adopt Dial's connection (already subscribed above)
		}
		closed, err := b.establish()
		if err != nil {
			return nil, err
		}
		if err := subscribe(); err != nil {
			// The connection established above but topology/subscribe failed: close
			// it so we don't leak a socket + its NotifyClose goroutine when the
			// supervisor backs off and re-dials a fresh connection.
			_ = b.Close()
			return nil, err
		}
		return closed, nil
	}
	sup := newSupervisor(connect, defaultBackoff(), onState, log)
	go sup.Run(ctx)
	return b.out, nil
}

// consumeRaw starts a manual-ack consumer on the current channel and returns the
// raw amqp delivery channel (the pump adapts it). Manual-ack: the Handler acks
// only after the local state change commits (NFR6).
func (b *Bus) consumeRaw(queueName string) (<-chan amqp.Delivery, error) {
	ch := b.curCh()
	if ch == nil {
		return nil, errors.New("consume: no broker channel")
	}
	return ch.Consume(queueName, "", false, false, false, false, nil)
}

// pump forwards one generation's deliveries into the stable out channel until the
// raw channel closes (connection lost) or ctx is cancelled. b.out is never closed
// here, so a reconnect's new pump simply resumes feeding the same Handler.
func (b *Bus) pump(ctx context.Context, raw <-chan amqp.Delivery) {
	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-raw:
			if !ok {
				return // this generation's connection dropped; supervisor reconnects
			}
			select {
			case b.out <- amqpDelivery{d: d}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Publish sends a persistent JSON message to the service's OWN exchange with the
// given routing key (the 1 s heartbeat), using the CURRENT channel. A publish
// while disconnected returns an error (the heartbeat then logs+skips, leaving the
// liveness file stale — the honest bus-down signal).
func (b *Bus) Publish(ctx context.Context, routingKey string, body []byte) error {
	ch := b.curCh()
	if ch == nil {
		return errors.New("publish: no broker channel")
	}
	return ch.PublishWithContext(ctx, b.exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
}

// Close shuts the channel then the connection, in that order.
func (b *Bus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
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
