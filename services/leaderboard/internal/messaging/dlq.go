package messaging

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Consumer-side DLQ topology (Story 1.9). The blueprint mandates: a poison or
// transient-failing message dead-letters to the consumer's DLX, is retried from a
// single `.retry` queue with per-hop exponential backoff (per-message TTL,
// dead-lettering back to the work queue), and on exceeding the delivery-count cap
// lands terminally in `.parking` (+ a Control-Room alert). Classic queues
// (Q&A Round 27). Names follow AR10: DLX `<consumer>.dlx`,
// retry `<consumer>.<purpose>.retry`, parking `<consumer>.<purpose>.parking`.
const (
	// LeaderboardDLXExchange is the consumer-side dead-letter exchange (a DIRECT
	// exchange — precise key routing), distinct from the service's own
	// LeaderboardExchange ("leaderboard.events", where heartbeats publish).
	LeaderboardDLXExchange = "leaderboard.dlx"

	// Routing keys on the DLX. retry → the retry queue; redeliver → the work
	// queue (where the retry queue dead-letters a message back after its TTL);
	// park → the terminal parking queue.
	dlqRetryRoutingKey     = "retry"
	dlqRedeliverRoutingKey = "redeliver"
	dlqParkRoutingKey      = "park"

	// Message headers. retryCountHeader carries the hop count across the retry
	// round-trip (this IS the "aggregate x-death count" of the architecture, made
	// explicit + deterministic). ParkReasonHeader records why a message was
	// quarantined — exported because it is observable on every parked message
	// (Control Room / E12 reads it off the parking queue).
	retryCountHeader = "x-pitwall-retry-count"
	ParkReasonHeader = "x-pitwall-park-reason"
)

// RetryQueueName / ParkingQueueName derive the DLQ queue names from the work
// queue (`<work>.retry`, `<work>.parking`) so the topology is consistent.
func RetryQueueName(workQueue string) string   { return workQueue + ".retry" }
func ParkingQueueName(workQueue string) string { return workQueue + ".parking" }

// RetryCount reports how many times this delivery has already been retried
// (header retryCountHeader; absent ⇒ 0).
func (a amqpDelivery) RetryCount() int { return parseRetryCount(a.d.Headers) }

// parseRetryCount reads the retry-count header defensively: amqp091 may hand an
// integer header back as int32 / int64 / int. Any missing or non-integer value
// is treated as 0 (a fresh, never-retried message).
func parseRetryCount(headers amqp.Table) int {
	if headers == nil {
		return 0
	}
	switch v := headers[retryCountHeader].(type) {
	case int32:
		return int(v)
	case int64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

// buildDLXPublishing builds the persistent message for a DLX publish. For a RETRY
// it carries a per-message Expiration (the exponential backoff TTL, ms) and the
// incremented retry-count header; for a PARK it omits Expiration and stamps the
// reason header. Pure (no I/O) so it is unit-tested without a broker.
func buildDLXPublishing(body []byte, expirationMs, retryCount int, parkReason string) amqp.Publishing {
	h := amqp.Table{retryCountHeader: int32(retryCount)}
	if parkReason != "" {
		h[ParkReasonHeader] = parkReason
	}
	p := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
		Headers:      h,
	}
	if expirationMs > 0 {
		p.Expiration = strconv.Itoa(expirationMs)
	}
	return p
}

// DeclareDLQTopology declares the full consumer-side DLQ topology for the given
// work queue and binds the work queue to its source exchange. It supersedes
// DeclareConsumerQueue for the production path: the work queue is declared WITH
// dead-letter args (a one-time, self-healing migration from the arg-less 1.7/1.8
// queue — see declareWorkQueueResilient). Idempotent on a fresh broker.
func (b *Bus) DeclareDLQTopology(opts ConsumerOptions) error {
	ch := b.curCh()
	if ch == nil {
		return errChannelGone
	}
	retryQueue := RetryQueueName(opts.QueueName)
	parkingQueue := ParkingQueueName(opts.QueueName)

	// Producer's exchange (so binding works even if we start before the producer).
	if err := ch.ExchangeDeclare(opts.SourceExchange, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	// The consumer-side dead-letter exchange (direct: route by retry/redeliver/park).
	if err := ch.ExchangeDeclare(LeaderboardDLXExchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}
	// Retry queue: no consumer; per-message TTL governs the delay; on expiry it
	// dead-letters back to the work queue via the DLX's `redeliver` key.
	if _, err := ch.QueueDeclare(retryQueue, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    LeaderboardDLXExchange,
		"x-dead-letter-routing-key": dlqRedeliverRoutingKey,
	}); err != nil {
		return err
	}
	// Parking queue: terminal quarantine — no dead-letter args, no consumer.
	if _, err := ch.QueueDeclare(parkingQueue, true, false, false, false, nil); err != nil {
		return err
	}
	// Work queue WITH dead-letter args (safety net: an unmodelled reject parks
	// rather than drops). Classic queue args are immutable, so redeclaring the
	// arg-less 1.7/1.8 queue needs a delete+redeclare — handled resiliently.
	if err := b.declareWorkQueueResilient(opts.QueueName, amqp.Table{
		"x-dead-letter-exchange":    LeaderboardDLXExchange,
		"x-dead-letter-routing-key": dlqParkRoutingKey,
	}); err != nil {
		return err
	}
	// DLX bindings: retry→retry queue, redeliver→work queue, park→parking queue.
	for _, bind := range []struct{ key, queue string }{
		{dlqRetryRoutingKey, retryQueue},
		{dlqRedeliverRoutingKey, opts.QueueName},
		{dlqParkRoutingKey, parkingQueue},
	} {
		if err := ch.QueueBind(bind.queue, bind.key, LeaderboardDLXExchange, false, nil); err != nil {
			return err
		}
	}
	// Work queue ← source exchange, on every routing key (one queue preserves
	// publish order across lap.recorded + session.*).
	for _, key := range opts.RoutingKeys {
		if err := ch.QueueBind(opts.QueueName, key, opts.SourceExchange, false, nil); err != nil {
			return err
		}
	}
	b.dlx = LeaderboardDLXExchange
	return ch.Qos(opts.Prefetch, 0, false)
}

// declareWorkQueueResilient declares the work queue with the given args, healing
// the classic-queue immutable-args constraint: if an arg-less queue of the same
// name already exists (1.7/1.8 dev brokers), declare-with-args raises a 406
// PRECONDITION_FAILED that closes the channel — so the probe runs on a throwaway
// channel, and on 406 we delete the old queue and redeclare with args on a fresh
// channel. Fresh brokers (CI / testcontainers) simply declare. The main channel
// (b.ch) is never poisoned by the probe.
//
// The delete is **empty-only** (`ifEmpty=true`): we never drop a buffered-but-
// unconsumed message during the one-time migration, and we do NOT assume any peer
// will re-emit it (golden rule). If the stale queue still holds messages, we refuse
// loudly so an operator drains it first — "never silently dropped" over convenience.
func (b *Bus) declareWorkQueueResilient(name string, args amqp.Table) error {
	conn := b.curConn()
	if conn == nil {
		return errChannelGone
	}
	probe, err := conn.Channel()
	if err != nil {
		return err
	}
	if _, derr := probe.QueueDeclare(name, true, false, false, false, args); derr == nil {
		return probe.Close()
	} else if !isPreconditionFailed(derr) {
		_ = probe.Close()
		return derr
	}
	// 406: the probe channel is already closed by the broker. Migrate the arg-less
	// 1.7/1.8 queue to one carrying the DLX args by delete + redeclare on a fresh
	// channel — but only if it is EMPTY, so no in-flight message is ever lost.
	cleaner, cerr := conn.Channel()
	if cerr != nil {
		return cerr
	}
	defer func() { _ = cleaner.Close() }()
	if _, delErr := cleaner.QueueDelete(name, false, true, false); delErr != nil {
		return fmt.Errorf("cannot migrate queue %q to dead-letter args: it exists with stale args and is "+
			"non-empty; drain it (or delete it manually) then restart — refusing to drop buffered messages: %w",
			name, delErr)
	}
	if _, reErr := cleaner.QueueDeclare(name, true, false, false, false, args); reErr != nil {
		return reErr
	}
	return nil
}

// isPreconditionFailed reports whether err is an AMQP 406 (inequivalent queue
// args) channel exception.
func isPreconditionFailed(err error) bool {
	var amqpErr *amqp.Error
	return errors.As(err, &amqpErr) && amqpErr.Code == amqp.PreconditionFailed
}

// PublishToDLX publishes body to the consumer's DLX with the given routing key
// (dlqRetryRoutingKey / dlqParkRoutingKey). For a retry, expirationMs is the
// per-hop backoff TTL and retryCount the incremented hop count; for a park,
// expirationMs is 0 and parkReason records why. Manual-ack discipline: the
// caller acks the ORIGINAL delivery only after this republish succeeds.
func (b *Bus) PublishToDLX(ctx context.Context, routingKey string, body []byte, expirationMs, retryCount int, parkReason string) error {
	ch := b.curCh()
	if ch == nil {
		return errChannelGone
	}
	return ch.PublishWithContext(ctx, b.dlx, routingKey, false, false,
		buildDLXPublishing(body, expirationMs, retryCount, parkReason))
}

// RetryToDLX republishes a failed message to the retry queue with the backoff TTL.
func (b *Bus) RetryToDLX(ctx context.Context, body []byte, delayMs, nextRetries int) error {
	return b.PublishToDLX(ctx, dlqRetryRoutingKey, body, delayMs, nextRetries, "")
}

// ParkToDLX routes a message terminally to the parking queue with a reason.
func (b *Bus) ParkToDLX(ctx context.Context, body []byte, reason string) error {
	return b.PublishToDLX(ctx, dlqParkRoutingKey, body, 0, 0, reason)
}
