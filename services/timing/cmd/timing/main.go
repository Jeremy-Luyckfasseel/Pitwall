// Command timing is the Go service skeleton on the bus. It connects to RabbitMQ,
// emits a 1 s control.heartbeat to its own timing.events exchange (Story 1.3),
// logs structured JSON, and shuts down gracefully on SIGTERM. Story 1.4 adds the
// reliability spine: a private SQLite database with a transactional outbox and a
// background relay that publishes durably (sent only after a broker ack) and
// survives a bus outage. Domain logic (laps, sessions, simulator) lands in 1.5+.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/dlq"
	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/config"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/heartbeat"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/relay"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/simulator"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		// Bootstrap logger (config failed): still structured JSON, no secrets.
		logging.New(os.Stderr, "timing", uuid.NewString(), "error").
			Error("configuration error", "error", err.Error())
		return 1
	}

	// One correlationId per process lifecycle, carried on every log line.
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

	// Private datastore: open + run migrations + create the outbox (Story 1.4).
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
	outbox := persistence.NewOutboxStore(db)
	log.Info("database ready; migrations applied", "path", cfg.DBPath)

	log.Info("connecting to broker", "host", cfg.RabbitHost, "port", cfg.RabbitPort, "vhost", cfg.RabbitVHost)
	pub, err := messaging.Dial(cfg.AMQPURI(), messaging.TimingExchange)
	if err != nil {
		log.Error("failed to connect to broker", "error", err.Error())
		return 1
	}
	log.Info("connected; exchange declared", "exchange", messaging.TimingExchange, "instanceId", instanceID)

	emitter := &heartbeat.Emitter{
		Interval:     time.Duration(cfg.HeartbeatInterval) * time.Millisecond,
		LivenessFile: cfg.LivenessFile,
		Build: func(now time.Time) messaging.Envelope {
			return messaging.NewHeartbeatEnvelope(cfg.ServiceName, instanceID, correlationID, now)
		},
		Validate: validator.ValidateHeartbeat,
		Publish:  pub.Publish,
		Log:      log,
	}

	outboxRelay := relay.New(relay.Config{
		Store:    outbox,
		Validate: validator.ValidateEnvelopeBytes,
		Publish:  pub.PublishConfirmed, // reconnect-aware: uses the current confirm channel
		Interval: time.Duration(cfg.OutboxPollInterval) * time.Millisecond,
		Log:      log,
	})

	// The producer seam: durably enqueue an event (own tx) + kick the relay.
	enqueue := relay.NewEnqueuer(db, outbox, validator.ValidateEnvelopeBytes, outboxRelay)

	// Simulator (Story 1.5): env-toggled, OFF by default. When on, it generates
	// continuous sessions of gate check-ins + laps for N drivers through the outbox.
	//
	// Story 2.3 makes Timing dual-role for the register-first chain (Q&A Round 32): the
	// simulator RESOLVES each driver's canonical masterId via Identity before check-in.
	// That needs (a) a publisher to frontend.events for identity.lookup_requested (the
	// Frontend stand-in) and (b) a consumer of identity.resolved off identity.events that
	// signals the waiting Resolve. Both are wired only when the simulator is on (the only
	// producer of register-first lookups in this story).
	var (
		sim            *simulator.Simulator
		consumerBus    *messaging.Bus
		lookupPub      *messaging.Publisher
		resolveHnd     *consumer.Handler
		consumerOpts   messaging.ConsumerOptions
		prConsumerBus  *messaging.Bus
		prRefreshHnd   *consumer.PRRefreshHandler
		prConsumerOpts messaging.ConsumerOptions
	)
	if cfg.SimulatorEnabled {
		tpStore := persistence.NewTransponderStore(db)
		heldStore := persistence.NewHeldLineScanStore(db)
		prStore := persistence.NewDriverPRStore(db)
		outageStore := persistence.NewScannerOutageStore(db)

		lookupPub, err = messaging.Dial(cfg.AMQPURI(), messaging.FrontendEventsExchange)
		if err != nil {
			log.Error("failed to connect lookup publisher to broker", "error", err.Error())
			return 1
		}
		consumerBus, err = messaging.DialConsumer(cfg.AMQPURI(), messaging.TimingExchange)
		if err != nil {
			log.Error("failed to connect identity.resolved consumer to broker", "error", err.Error())
			_ = lookupPub.Close()
			return 1
		}

		resolver := &consumer.Resolver{
			DB:     db,
			Source: "frontend", // the simulator impersonates the Frontend registration producer (Q30.1)
			Log:    log,
			Publish: func(ctx context.Context, env messaging.Envelope) error {
				body, merr := json.Marshal(env)
				if merr != nil {
					return consumer.Permanent(merr) // a malformed envelope cannot be fixed by retrying
				}
				if verr := validator.ValidateEnvelopeBytes(body); verr != nil { // validate-on-publish
					return consumer.Permanent(verr) // a contract-invalid lookup is permanent — fail fast
				}
				return lookupPub.Publish(ctx, env.Type, body) // a broker error is transient — Resolve retries
			},
		}

		resolveHnd = &consumer.Handler{
			Validate:    validator.ValidateEnvelopeBytes,
			Deliverer:   resolver,
			Log:         log,
			ResolvedKey: messaging.IdentityResolvedRoutingKey,
			Policy: dlq.Policy{
				MaxAttempts: cfg.DLQMaxAttempts,
				BaseMs:      cfg.DLQRetryBaseMs,
				Multiplier:  cfg.DLQRetryMultiplier,
				MaxMs:       cfg.DLQRetryMaxMs,
			},
			Retry: consumerBus.RetryToDLX,
			Park:  consumerBus.ParkToDLX,
		}
		consumerOpts = messaging.ConsumerOptions{
			SourceExchange: messaging.IdentityEventsExchange, // bind to Identity's exchange for the reply
			QueueName:      messaging.IdentityResolvedQueue,
			RoutingKeys:    []string{messaging.IdentityResolvedRoutingKey},
			Prefetch:       cfg.ConsumePrefetch,
			DLXExchange:    messaging.TimingDLXExchange,
		}

		sim = simulator.New(simulator.Config{
			Drivers:      cfg.SimDrivers,
			Transponders: cfg.SimTransponders,
			LapMeanMs:    cfg.SimLapMeanMs,
			LapStddevMs:  cfg.SimLapStddevMs,
			SessionLaps:  cfg.SimSessionLaps,
			MinLapTimeMs: cfg.MinLapTimeMs,
			Tick:         time.Duration(cfg.SimTickMs) * time.Millisecond,
			SessionGap:   time.Duration(cfg.SimSessionGapMs) * time.Millisecond,
			Source:       cfg.ServiceName,
			Rng:          seedRNG(cfg),
			Now:          time.Now,
			Enqueue:      enqueue,
			Resolve:      resolver.Resolve,
			AssignTransponder: func(ctx context.Context, transponderID, masterID string) (bool, string, error) {
				return tpStore.Assign(ctx, transponderID, masterID, messaging.FormatWireTime(time.Now()))
			},
			UnknownTokenScans: cfg.UnknownTokenScans,
			RecordHeldScan: func(ctx context.Context, token, method, sessionID, occurredAt, reason string) error {
				return heldStore.Record(ctx, token, method, sessionID, occurredAt, reason, messaging.FormatWireTime(time.Now()))
			},
			// Live PR detection (Story 3.4, FR37): consult Timing's local PR copy per
			// counted lap; Run enqueues personal_record.broken on a break. Gated with the
			// simulator (the only lap source today), like every other lap-path seam.
			ObservePR: func(ctx context.Context, masterID, sessionID string, lapTimeMs int64, at string) (bool, *int64, error) {
				return prStore.ObserveLap(ctx, masterID, sessionID, lapTimeMs, at)
			},
			// Scanner-offline (Story 3.5, FR38): inject an outage window that drops crossings
			// (the honest gap, never faked) and persist it durably. Sim-gated like every other
			// lap-path seam; the migration is unconditional (always applied by Open).
			ScannerOutageLaps: cfg.SimScannerOutageLaps,
			OpenOutage: func(ctx context.Context, scannerID, sessionID, gapFrom, since, recordedAt string) (int64, error) {
				return outageStore.OpenOutage(ctx, scannerID, sessionID, gapFrom, since, recordedAt)
			},
			CloseOutage: outageStore.CloseOutage,
			Log:         log,
		})
		log.Info("simulator enabled (register-first)", "drivers", cfg.SimDrivers, "transponders", cfg.SimTransponders,
			"sessionLaps", cfg.SimSessionLaps, "lapMeanMs", cfg.SimLapMeanMs, "lapStddevMs", cfg.SimLapStddevMs,
			"minLapTimeMs", cfg.MinLapTimeMs, "unknownTokenScans", cfg.UnknownTokenScans,
			"scannerOutageLaps", cfg.SimScannerOutageLaps)

		// PR refresh consumer (Story 3.4, AC2): Timing consumes driver.pr_updated off
		// driver.events to refresh its local PR copy. The Go consumer runtime is
		// single-binding-per-queue, so this is a SECOND consumer (own Bus + queue + DLX),
		// distinct from the identity.resolved one; both are simulator-gated (the PR copy is
		// only meaningful when detection runs).
		prConsumerBus, err = messaging.DialConsumer(cfg.AMQPURI(), messaging.TimingExchange)
		if err != nil {
			log.Error("failed to connect driver.pr_updated consumer to broker", "error", err.Error())
			_ = lookupPub.Close()
			_ = consumerBus.Close()
			return 1
		}
		prRefreshHnd = &consumer.PRRefreshHandler{
			Validate:  validator.ValidateEnvelopeBytes,
			Refresher: &consumer.PRStoreRefresher{Store: prStore, Now: func() string { return messaging.FormatWireTime(time.Now()) }},
			Log:       log,
			Key:       messaging.DriverPRUpdatedRoutingKey,
			Policy: dlq.Policy{
				MaxAttempts: cfg.DLQMaxAttempts,
				BaseMs:      cfg.DLQRetryBaseMs,
				Multiplier:  cfg.DLQRetryMultiplier,
				MaxMs:       cfg.DLQRetryMaxMs,
			},
			Retry: prConsumerBus.RetryToDLX,
			Park:  prConsumerBus.ParkToDLX,
		}
		prConsumerOpts = messaging.ConsumerOptions{
			SourceExchange: messaging.DriverEventsExchange,
			QueueName:      messaging.DriverPRUpdatedQueue,
			RoutingKeys:    []string{messaging.DriverPRUpdatedRoutingKey},
			Prefetch:       cfg.ConsumePrefetch,
			DLXExchange:    messaging.TimingDLXExchange,
		}
	}

	// Run until SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Supervise the broker connection (Story 1.10): on a mid-session bus kill the
	// supervisor re-dials with backoff and re-declares the exchange + confirm
	// channel; the relay (retry-forever) then flushes the outbox on reconnect. No
	// stale flag here — that is the Leaderboard's spectator-facing concern.
	if err := pub.ConnectAndServe(ctx, log, func(connected bool) {
		if connected {
			log.Info("broker connection established", "exchange", messaging.TimingExchange)
		} else {
			log.Warn("broker connection lost; buffering in the outbox until reconnect")
		}
	}); err != nil {
		log.Error("failed to start the broker connection supervisor", "error", err.Error())
		_ = pub.Close()
		return 1
	}

	// Register-first wiring (Story 2.3): the identity.resolved consumer + the
	// frontend.events lookup publisher must be live BEFORE the simulator's Prepare
	// resolves driver ids (Prepare publishes a lookup and blocks on the reply).
	var consumerDone chan struct{}
	var prConsumerDone chan struct{}
	if sim != nil {
		if err := lookupPub.ConnectAndServe(ctx, log, func(connected bool) {
			if connected {
				log.Info("lookup publisher connection established", "exchange", messaging.FrontendEventsExchange)
			} else {
				log.Warn("lookup publisher connection lost; register-first lookups pause until reconnect")
			}
		}); err != nil {
			log.Error("failed to start the lookup publisher supervisor", "error", err.Error())
			_ = pub.Close()
			_ = lookupPub.Close()
			_ = consumerBus.Close()
			return 1
		}
		deliveries, derr := consumerBus.ConnectAndConsume(ctx, consumerOpts, log, func(connected bool) {
			if connected {
				log.Info("broker connection established; consuming identity.resolved", "queue", messaging.IdentityResolvedQueue)
			} else {
				log.Warn("broker connection lost; identity.resolved will redeliver on reconnect")
			}
		})
		if derr != nil {
			log.Error("failed to start the identity.resolved consumer supervisor", "error", derr.Error())
			_ = pub.Close()
			_ = lookupPub.Close()
			_ = consumerBus.Close()
			return 1
		}
		log.Info("consuming identity.resolved", "queue", messaging.IdentityResolvedQueue,
			"source", messaging.IdentityEventsExchange, "dlx", messaging.TimingDLXExchange,
			"retryQueue", messaging.RetryQueueName(messaging.IdentityResolvedQueue),
			"parkingQueue", messaging.ParkingQueueName(messaging.IdentityResolvedQueue), "prefetch", cfg.ConsumePrefetch)
		consumerDone = make(chan struct{})
		go func() { resolveHnd.Run(ctx, deliveries); close(consumerDone) }()

		prDeliveries, prErr := prConsumerBus.ConnectAndConsume(ctx, prConsumerOpts, log, func(connected bool) {
			if connected {
				log.Info("broker connection established; consuming driver.pr_updated", "queue", messaging.DriverPRUpdatedQueue)
			} else {
				log.Warn("broker connection lost; driver.pr_updated will redeliver on reconnect")
			}
		})
		if prErr != nil {
			log.Error("failed to start the driver.pr_updated consumer supervisor", "error", prErr.Error())
			_ = pub.Close()
			_ = lookupPub.Close()
			_ = consumerBus.Close()
			_ = prConsumerBus.Close()
			return 1
		}
		log.Info("consuming driver.pr_updated", "queue", messaging.DriverPRUpdatedQueue,
			"source", messaging.DriverEventsExchange, "dlx", messaging.TimingDLXExchange,
			"retryQueue", messaging.RetryQueueName(messaging.DriverPRUpdatedQueue),
			"parkingQueue", messaging.ParkingQueueName(messaging.DriverPRUpdatedQueue), "prefetch", cfg.ConsumePrefetch)
		prConsumerDone = make(chan struct{})
		go func() { prRefreshHnd.Run(ctx, prDeliveries); close(prConsumerDone) }()
	}

	loopDone := make(chan struct{})
	go func() {
		_ = emitter.Run(ctx)
		close(loopDone)
	}()
	relayDone := make(chan struct{})
	go func() {
		_ = outboxRelay.Run(ctx)
		close(relayDone)
	}()
	var simDone chan struct{}
	if sim != nil {
		simDone = make(chan struct{})
		go func() {
			if err := sim.Run(ctx); err != nil {
				log.Error("simulator stopped with error", "error", err.Error())
			}
			close(simDone)
		}()
	}

	<-ctx.Done()
	log.Info("shutdown signal received; draining")

	// Graceful shutdown (NFR19): stop the simulator + heartbeat + relay loops
	// (bounded), make a best-effort final outbox flush, then close cleanly.
	shutdownCtx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.ShutdownTimeout)*time.Millisecond)
	defer cancel()
	if simDone != nil {
		waitFor(shutdownCtx, log, "simulator loop", simDone)
	}
	if consumerDone != nil {
		waitFor(shutdownCtx, log, "identity.resolved consumer loop", consumerDone)
	}
	if prConsumerDone != nil {
		waitFor(shutdownCtx, log, "driver.pr_updated consumer loop", prConsumerDone)
	}
	waitFor(shutdownCtx, log, "heartbeat loop", loopDone)
	waitFor(shutdownCtx, log, "relay loop", relayDone)

	flushOutbox(shutdownCtx, log, outboxRelay)

	if consumerBus != nil {
		if cerr := consumerBus.Close(); cerr != nil {
			log.Error("error closing identity.resolved consumer connection", "error", cerr.Error())
		}
	}
	if prConsumerBus != nil {
		if cerr := prConsumerBus.Close(); cerr != nil {
			log.Error("error closing driver.pr_updated consumer connection", "error", cerr.Error())
		}
	}
	if lookupPub != nil {
		if cerr := lookupPub.Close(); cerr != nil {
			log.Error("error closing lookup publisher connection", "error", cerr.Error())
		}
	}

	if err := pub.Close(); err != nil {
		log.Error("error closing broker connection", "error", err.Error())
		return 1
	}
	log.Info("shutdown complete")
	return 0
}

// waitFor blocks until done is closed or the bounded shutdown context expires.
func waitFor(ctx context.Context, log *slog.Logger, what string, done <-chan struct{}) {
	select {
	case <-done:
	case <-ctx.Done():
		log.Warn("loop did not stop within the shutdown timeout", "loop", what)
	}
}

// flushOutbox makes a bounded best-effort attempt to publish any rows still
// pending before the connection closes (Story 1.4 AC4). Whatever is not flushed
// stays durably pending for the next start — no loss either way.
func flushOutbox(ctx context.Context, log *slog.Logger, r *relay.Relay) {
	sent, remaining := r.Flush(ctx)
	log.Info("outbox flush complete", "flushed", sent, "pending", remaining)
}

// seedRNG builds the simulator's RNG: deterministic when SIM_SEED is set
// (reproducible demos), otherwise time-seeded.
func seedRNG(cfg *config.Config) *rand.Rand {
	if cfg.SimSeedSet {
		return rand.New(rand.NewSource(cfg.SimSeed))
	}
	return rand.New(rand.NewSource(time.Now().UnixNano()))
}
