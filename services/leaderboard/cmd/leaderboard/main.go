// Command leaderboard is the walking skeleton's first CONSUMER service. It
// connects to RabbitMQ, consumes timing's lap.recorded events off the
// timing.events exchange, dedupes them through a durable idempotent inbox,
// validates them against /contract on consume, folds them into a best-lap
// standings projection (SQLite), and serves a live trackside board (a
// //go:embed-ed Vite/React bundle pushed over SSE). Like every service it also
// emits a 1 s control.heartbeat to its OWN leaderboard.events exchange, logs
// structured JSON, and shuts down gracefully on SIGTERM. Session lifecycle
// (1.8), DLQ/retry (1.9) and stale-flag/replay (1.10) build on this.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/config"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/heartbeat"
	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/web"
)

// consumeQueue is this consumer's durable queue, bound to the producer's
// timing.events exchange on lap.recorded + session.started + session.ended
// (one queue preserves the producer's publish order across event types). The
// name predates the session bindings (Story 1.8) and is deliberately kept —
// renaming would orphan the existing durable queue on dev brokers. Story 1.9
// redeclares it WITH dead-letter args via DeclareDLQTopology (self-healing the
// classic-queue immutable-args constraint — see messaging/dlq.go).
const consumeQueue = "leaderboard.lap-recorded"

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logging.New(os.Stderr, "leaderboard", uuid.NewString(), "error").
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
	log.Info("database ready; migrations applied", "path", cfg.DBPath)

	// Seed the ingest sequence so the tie-break stays monotonic across restarts.
	startSeq, err := store.MaxSeq(context.Background())
	if err != nil {
		log.Error("cannot read max ingest sequence", "error", err.Error())
		return 1
	}

	log.Info("connecting to broker", "host", cfg.RabbitHost, "port", cfg.RabbitPort, "vhost", cfg.RabbitVHost)
	bus, err := messaging.Dial(cfg.AMQPURI(), messaging.LeaderboardExchange)
	if err != nil {
		log.Error("failed to connect to broker", "error", err.Error())
		return 1
	}
	log.Info("connected; own exchange declared", "exchange", messaging.LeaderboardExchange, "instanceId", instanceID)

	consumerOpts := messaging.ConsumerOptions{
		SourceExchange: cfg.TimingExchange,
		QueueName:      consumeQueue,
		RoutingKeys: []string{
			messaging.LapRecordedRoutingKey,
			messaging.SessionStartedRoutingKey,
			messaging.SessionEndedRoutingKey,
		},
		Prefetch: cfg.ConsumePrefetch,
	}

	// busConnected drives the served bundle's stale flag (Story 1.10). It starts
	// true (Dial succeeded), and the connection supervisor flips it on each
	// connected<->lost transition. A bus-down freezes the board on last-known and
	// flags it reconnecting — honest degradation, never faked-live (FR47, C1).
	var busConnected atomic.Bool
	busConnected.Store(true)

	// The live board: serves the embedded SPA + pushes standings over SSE. The
	// snapshot func reads the CURRENT session's board on demand (the board owns
	// no state; the auto-reset IS this read switching to the newest session).
	// On a transient read error it serves the LAST-KNOWN-GOOD snapshot instead
	// of the waiting state — a DB hiccup must never visibly wipe a live board
	// (session:null is reserved for "no session ever seen"). Every served bundle
	// carries the current bus-connection state (stale flag).
	var snapMu sync.Mutex
	lastGood := web.ToSnapshot(nil)
	snapshot := func() web.Snapshot {
		b, serr := store.CurrentBoard(context.Background())
		snapMu.Lock()
		if serr != nil {
			log.Error("failed to read the current board for snapshot; serving last-known-good", "error", serr.Error())
			snap := lastGood
			snapMu.Unlock()
			return snap.WithConnection(busConnected.Load())
		}
		lastGood = web.ToSnapshot(b) // nil board = no session ever seen (waiting state)
		snap := lastGood
		snapMu.Unlock()
		return snap.WithConnection(busConnected.Load())
	}
	server := web.NewServer(cfg.HTTPAddr, snapshot, log)

	handler := &consumer.Handler{
		Validate: validator.ValidateEnvelopeBytes,
		Store:    store,
		Log:      log,
		Notify:   server.Publish, // push the new standings to all SSE clients
		StartSeq: startSeq,
		// DLQ wiring (Story 1.9): a processing failure retries with exponential
		// backoff then parks; an invalid/poison message parks immediately.
		Policy: domain.DLQPolicy{
			MaxAttempts: cfg.DLQMaxAttempts,
			BaseMs:      cfg.DLQRetryBaseMs,
			Multiplier:  cfg.DLQRetryMultiplier,
			MaxMs:       cfg.DLQRetryMaxMs,
		},
		Retry: bus.RetryToDLX,
		Park:  bus.ParkToDLX,
	}

	emitter := &heartbeat.Emitter{
		Interval:     time.Duration(cfg.HeartbeatInterval) * time.Millisecond,
		LivenessFile: cfg.LivenessFile,
		Build: func(now time.Time) messaging.Envelope {
			return messaging.NewHeartbeatEnvelope(cfg.ServiceName, instanceID, correlationID, now)
		},
		Validate: validator.ValidateHeartbeat,
		Publish:  bus.Publish,
		Log:      log,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Supervise the broker connection + consumer (Story 1.10): declare the DLQ
	// topology, consume, and on a mid-session bus kill re-dial + re-declare +
	// re-subscribe, pumping into a STABLE deliveries channel so the Handler keeps
	// running across reconnects. The state callback flips the stale flag and
	// pushes a fresh bundle so connected boards see the change immediately.
	deliveries, err := bus.ConnectAndConsume(ctx, consumerOpts, log, func(connected bool) {
		busConnected.Store(connected)
		if connected {
			log.Info("broker connection established; board live", "queue", consumeQueue)
		} else {
			log.Warn("broker connection lost; board frozen on last-known, flagged reconnecting")
		}
		server.Publish() // push the stale-flag transition to connected SSE clients
	})
	if err != nil {
		log.Error("failed to start the consumer connection supervisor", "error", err.Error())
		_ = bus.Close()
		return 1
	}
	log.Info("consuming", "queue", consumeQueue, "source", cfg.TimingExchange,
		"dlx", messaging.LeaderboardDLXExchange,
		"retryQueue", messaging.RetryQueueName(consumeQueue),
		"parkingQueue", messaging.ParkingQueueName(consumeQueue),
		"prefetch", cfg.ConsumePrefetch)

	hbDone := make(chan struct{})
	go func() { _ = emitter.Run(ctx); close(hbDone) }()
	consumerDone := make(chan struct{})
	go func() { handler.Run(ctx, deliveries); close(consumerDone) }()
	webDone := make(chan struct{})
	go func() {
		if werr := server.Run(ctx); werr != nil {
			log.Error("http/sse server stopped with error", "error", werr.Error())
		}
		close(webDone)
	}()
	log.Info("leaderboard up", "httpAddr", cfg.HTTPAddr)

	<-ctx.Done()
	log.Info("shutdown signal received; draining")

	shutdownCtx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.ShutdownTimeout)*time.Millisecond)
	defer cancel()
	waitFor(shutdownCtx, log, "consumer loop", consumerDone)
	waitFor(shutdownCtx, log, "heartbeat loop", hbDone)
	waitFor(shutdownCtx, log, "http/sse server", webDone)

	if cerr := bus.Close(); cerr != nil {
		log.Error("error closing broker connection", "error", cerr.Error())
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
