// Command identity is Pitwall's canonical-id issuer (Story 2.2, FR1/FR2/FR3/NFR15,
// ADR-0003). It CONSUMES identity.lookup_requested off the originating service's
// exchange (frontend.events; Q&A Round 30), resolves-or-mints exactly one masterId per
// email (de-duplicated on the email natural key), and PUBLISHES identity.resolved to
// its OWN identity.events exchange via the transactional outbox + relay — so the reply
// survives a bus outage. It is the first service that is both a consumer and an
// outbox-backed producer.
//
// Like every service it emits a 1 s control.heartbeat to its own exchange, validates
// every message in and out against /contract, dedupes redeliveries through an
// idempotent inbox, dead-letters poison via the DLQ/retry/parking topology, logs
// structured JSON, and shuts down gracefully on SIGTERM. All blueprint mechanics come
// from libs/go-pitwall (Story 2.1); this wires Identity's domain + topology onto them.
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	goperasure "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/erasure"
	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/logging"
	librelay "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/relay"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/config"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/heartbeat"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/persistence"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logging.New(os.Stderr, "identity", uuid.NewString(), "error").
			Error("configuration error", "error", err.Error())
		return 1
	}

	correlationID := uuid.NewString()
	log := logging.New(os.Stdout, cfg.ServiceName, correlationID, cfg.LogLevel)

	instanceID := cfg.InstanceID
	if instanceID == "" {
		instanceID = uuid.NewString()
	}

	contractDir, err := messaging.ResolveContractDir(cfg.ContractDir)
	if err != nil {
		log.Error("cannot locate /contract", "error", err.Error())
		return 1
	}
	validator, err := messaging.NewValidator(contractDir)
	if err != nil {
		log.Error("cannot compile contract schemas", "error", err.Error())
		return 1
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.ShutdownTimeout)*time.Millisecond)
	db, err := persistence.Open(dbCtx, cfg.DBPath)
	dbCancel()
	if err != nil {
		log.Error("cannot open database", "error", err.Error(), "path", cfg.DBPath)
		return 1
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			log.Error("error closing database", "error", cerr.Error())
		}
	}()
	store := persistence.NewStore(db)
	outbox := persistence.NewOutboxStore(db)
	erasureStore := persistence.NewErasureStore()
	heldStore := persistence.NewHeldLookupStore()
	log.Info("database ready; migrations applied", "path", cfg.DBPath)

	// Dual role (see package doc): a CONSUMER bus (binds frontend.events, publishes the
	// heartbeat to identity.events) AND a confirm PUBLISHER (drives the outbox relay
	// that publishes identity.resolved to identity.events). Both declare identity.events
	// idempotently. Reuses both proven libs/go-pitwall constructors — no lib change.
	log.Info("connecting to broker", "host", cfg.RabbitHost, "port", cfg.RabbitPort, "vhost", cfg.RabbitVHost)
	consumerBus, err := messaging.DialConsumer(cfg.AMQPURI(), messaging.IdentityExchange)
	if err != nil {
		log.Error("failed to connect consumer to broker", "error", err.Error())
		return 1
	}
	publisher, err := messaging.DialPublisher(cfg.AMQPURI(), messaging.IdentityExchange)
	if err != nil {
		log.Error("failed to connect publisher to broker", "error", err.Error())
		_ = consumerBus.Close()
		return 1
	}
	log.Info("connected; own exchange declared", "exchange", messaging.IdentityExchange, "instanceId", instanceID)

	// The outbox relay: validate-on-publish, publish via the reconnect-aware confirm
	// channel, mark sent only after a broker ack. Survives a bus outage (retry-forever).
	outboxRelay := librelay.New(librelay.Config{
		Store:    outbox,
		Validate: validator.ValidateEnvelopeBytes,
		Publish:  publisher.PublishConfirmed,
		Interval: time.Duration(cfg.OutboxPollInterval) * time.Millisecond,
		Log:      log,
	})

	// The resolve-or-mint engine: one tx per lookup (inbox-dedupe → email-suppression
	// gate (Story 2.6, AC2) → resolve-or-mint → enqueue the identity.resolved reply →
	// mark inbox). The reply is published by the relay above.
	resolver := &consumer.TxResolver{
		DB:          db,
		Store:       store,
		Outbox:      outbox,
		ValidateOut: validator.ValidateEnvelopeBytes,
		MintID:      uuid.NewString, // lowercase canonical UUID v4 — the canonical masterId
		Source:      cfg.ServiceName,
		ResolvedKey: messaging.ResolvedRoutingKey,
		IsEmailSuppressed: func(ctx context.Context, tx *sql.Tx, emailHash string) (bool, error) {
			return erasureStore.IsEmailSuppressed(ctx, tx, emailHash)
		},
		RecordHeld: func(ctx context.Context, tx *sql.Tx, requestID, emailHash, occurredAt, recordedAt string) error {
			return heldStore.Record(ctx, tx, requestID, emailHash, "email suppressed by prior erasure", occurredAt, recordedAt)
		},
	}

	// The erasure engine (Story 2.6, AC1): libs/go-pitwall/erasure.Handler's reusable
	// inbox-dedupe → delete-slice → tombstone → atomically-enqueued ack pipeline, with
	// Identity's own delete-slice/tombstone callbacks bound to erasureStore. Delete looks
	// up the CURRENT email before erasing it (so it can suppress the hash) — a
	// not-found (already-erased / never-existed) masterId is a graceful no-op, never an
	// error (idempotent double-erasure). Enqueue reuses the SAME outbox/validator as
	// identity.resolved — one relay drains both event types.
	erasureHandler := &goperasure.Handler{
		DB:      db,
		Service: cfg.ServiceName,
		Mode:    goperasure.ModeDeleted,
		Delete: func(ctx context.Context, tx *sql.Tx, masterID string) error {
			email, found, err := erasureStore.LookupEmail(ctx, tx, masterID)
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
			hash := domain.HashEmail(domain.NormalizeEmail(email))
			return erasureStore.DeleteSlice(ctx, tx, masterID, hash, messaging.FormatWireTime(time.Now()))
		},
		Tombstone: func(ctx context.Context, tx *sql.Tx, masterID string) error {
			return erasureStore.WriteTombstone(ctx, tx, masterID, messaging.FormatWireTime(time.Now()))
		},
		Enqueue: func(ctx context.Context, tx *sql.Tx, ack messaging.Envelope) error {
			return librelay.EnqueueEnvelope(tx, outbox, validator.ValidateEnvelopeBytes, ack)
		},
	}

	// ONE queue carries both intents (they share the SAME producer exchange,
	// frontend.events, Q30.1) — the library's own DeclareConsumerQueue doc comment
	// states this is the intended design, so the producer's publish order is preserved.
	consumerOpts := messaging.ConsumerOptions{
		SourceExchange: cfg.SourceExchange, // frontend.events (the originating intent exchange)
		QueueName:      messaging.LookupRequestedQueue,
		RoutingKeys:    []string{messaging.LookupRequestedRoutingKey, goperasure.ErasureRequestedType},
		Prefetch:       cfg.ConsumePrefetch,
		DLXExchange:    messaging.IdentityDLXExchange,
	}

	handler := &consumer.Handler{
		Validate:   validator.ValidateEnvelopeBytes,
		Resolver:   resolver,
		Erasure:    erasureHandler,
		Log:        log,
		Notify:     outboxRelay.Kick, // a fresh reply/ack is enqueued; publish promptly
		LookupKey:  messaging.LookupRequestedRoutingKey,
		ErasureKey: goperasure.ErasureRequestedType,
		Policy: domain.DLQPolicy{
			MaxAttempts: cfg.DLQMaxAttempts,
			BaseMs:      cfg.DLQRetryBaseMs,
			Multiplier:  cfg.DLQRetryMultiplier,
			MaxMs:       cfg.DLQRetryMaxMs,
		},
		Retry: consumerBus.RetryToDLX,
		Park:  consumerBus.ParkToDLX,
	}

	emitter := &heartbeat.Emitter{
		Interval:     time.Duration(cfg.HeartbeatInterval) * time.Millisecond,
		LivenessFile: cfg.LivenessFile,
		Build: func(now time.Time) messaging.Envelope {
			return messaging.NewHeartbeatEnvelope(cfg.ServiceName, instanceID, correlationID, now)
		},
		Validate: validator.ValidateHeartbeat,
		Publish:  consumerBus.Publish,
		Log:      log,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Supervise the PUBLISHER connection (relay publishes identity.resolved across bus
	// kills — Story 1.10).
	if err := publisher.ConnectAndServe(ctx, log, func(connected bool) {
		if connected {
			log.Info("publisher connection established", "exchange", messaging.IdentityExchange)
		} else {
			log.Warn("publisher connection lost; buffering replies in the outbox until reconnect")
		}
	}); err != nil {
		log.Error("failed to start the publisher connection supervisor", "error", err.Error())
		_ = consumerBus.Close()
		_ = publisher.Close()
		return 1
	}

	// Supervise the CONSUMER connection: declare the DLQ topology, consume, and re-declare
	// + re-subscribe on every reconnect, pumping into a STABLE deliveries channel.
	deliveries, err := consumerBus.ConnectAndConsume(ctx, consumerOpts, log, func(connected bool) {
		if connected {
			log.Info("broker connection established; consuming", "queue", messaging.LookupRequestedQueue)
		} else {
			log.Warn("broker connection lost; lookups will redeliver on reconnect")
		}
	})
	if err != nil {
		log.Error("failed to start the consumer connection supervisor", "error", err.Error())
		_ = consumerBus.Close()
		_ = publisher.Close()
		return 1
	}
	log.Info("consuming", "queue", messaging.LookupRequestedQueue, "source", cfg.SourceExchange,
		"routingKeys", consumerOpts.RoutingKeys,
		"dlx", messaging.IdentityDLXExchange,
		"retryQueue", messaging.RetryQueueName(messaging.LookupRequestedQueue),
		"parkingQueue", messaging.ParkingQueueName(messaging.LookupRequestedQueue),
		"prefetch", cfg.ConsumePrefetch)

	hbDone := make(chan struct{})
	go func() { _ = emitter.Run(ctx); close(hbDone) }()
	relayDone := make(chan struct{})
	go func() { _ = outboxRelay.Run(ctx); close(relayDone) }()
	consumerDone := make(chan struct{})
	go func() { handler.Run(ctx, deliveries); close(consumerDone) }()
	log.Info("identity up", "service", cfg.ServiceName)

	<-ctx.Done()
	log.Info("shutdown signal received; draining")

	shutdownCtx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.ShutdownTimeout)*time.Millisecond)
	defer cancel()
	waitFor(shutdownCtx, log, "consumer loop", consumerDone)
	waitFor(shutdownCtx, log, "heartbeat loop", hbDone)
	waitFor(shutdownCtx, log, "relay loop", relayDone)

	// Best-effort final flush of any reply still pending before the connection closes
	// (Story 1.4 AC4). Whatever is not flushed stays durably pending for the next start.
	sent, remaining := outboxRelay.Flush(shutdownCtx)
	log.Info("outbox flush complete", "flushed", sent, "pending", remaining)

	var rc int
	if cerr := consumerBus.Close(); cerr != nil {
		log.Error("error closing consumer connection", "error", cerr.Error())
		rc = 1
	}
	if perr := publisher.Close(); perr != nil {
		log.Error("error closing publisher connection", "error", perr.Error())
		rc = 1
	}
	log.Info("shutdown complete")
	return rc
}

// waitFor blocks until done is closed or the bounded shutdown context expires.
func waitFor(ctx context.Context, log *slog.Logger, what string, done <-chan struct{}) {
	select {
	case <-done:
	case <-ctx.Done():
		log.Warn("loop did not stop within the shutdown timeout", "loop", what)
	}
}
