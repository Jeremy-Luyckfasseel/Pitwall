package messaging

import (
	amqp "github.com/rabbitmq/amqp091-go"
)

// Delivery is the broker-agnostic view of a consumed message the consumer logic
// works against. Keeping the consumer behind this interface lets it be unit
// tested with a fake delivery (no real RabbitMQ needed). Ack/Nack operate on the
// single current message (manual-ack discipline, NFR6).
//
//	Ack()            -> the message was processed (or was a dedupe no-op); drop it.
//	Nack(requeue)    -> processing failed. requeue=false discards/dead-letters it
//	                    (no DLX in Story 1.7 -> logged + dropped; Story 1.9 adds
//	                    the <consumer>.dlx so this becomes a true dead-letter).
type Delivery interface {
	Body() []byte
	Ack() error
	Nack(requeue bool) error
	// RetryCount is how many times this message has already been retried through
	// the DLQ (Story 1.9; 0 for a freshly-delivered message). It drives the
	// backoff/cap decision (domain.NextRetry).
	RetryCount() int
}

// amqpDelivery adapts an amqp091 delivery to the Delivery interface.
type amqpDelivery struct{ d amqp.Delivery }

func (a amqpDelivery) Body() []byte            { return a.d.Body }
func (a amqpDelivery) Ack() error              { return a.d.Ack(false) }
func (a amqpDelivery) Nack(requeue bool) error { return a.d.Nack(false, requeue) }

// ConsumerOptions configures the durable queue this service consumes from.
type ConsumerOptions struct {
	SourceExchange string   // the PRODUCER's exchange to bind to (e.g. "timing.events")
	QueueName      string   // this consumer's durable queue (e.g. "leaderboard.lap-recorded")
	RoutingKeys    []string // binding keys (e.g. lap.recorded, session.started, session.ended)
	Prefetch       int      // QoS: max in-flight unacked deliveries
}

// DeclareConsumerQueue declares the producer's exchange (idempotently, durable
// topic — so binding works even if this consumer starts before the producer),
// declares the durable queue, binds it on every routing key, and applies QoS.
// ONE queue carries all bound event types so the producer's publish order is
// preserved across session.* and lap.recorded (the normal path stays in-order;
// NFR24's out-of-order gating covers the exceptions, not the rule). Adding a
// binding to a live durable queue is legal; changing queue ARGS is not — Story
// 1.9 will REDECLARE this queue with x-dead-letter-exchange args (RabbitMQ
// cannot mutate queue args in place), a deliberate seam, see the story Dev Notes.
func (b *Bus) DeclareConsumerQueue(opts ConsumerOptions) error {
	if err := b.ch.ExchangeDeclare(opts.SourceExchange, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := b.ch.QueueDeclare(opts.QueueName, true, false, false, false, nil); err != nil {
		return err
	}
	for _, key := range opts.RoutingKeys {
		if err := b.ch.QueueBind(opts.QueueName, key, opts.SourceExchange, false, nil); err != nil {
			return err
		}
	}
	// Per-consumer prefetch; global=false.
	return b.ch.Qos(opts.Prefetch, 0, false)
}

// Consume starts a manual-ack consumer on the queue and returns a channel of
// broker-agnostic deliveries. autoAck is false: the caller acks only AFTER the
// state change is durably committed (NFR6).
func (b *Bus) Consume(queueName string) (<-chan Delivery, error) {
	raw, err := b.ch.Consume(queueName, "", false, false, false, false, nil)
	if err != nil {
		return nil, err
	}
	out := make(chan Delivery)
	go func() {
		defer close(out)
		for d := range raw {
			out <- amqpDelivery{d: d}
		}
	}()
	return out, nil
}
