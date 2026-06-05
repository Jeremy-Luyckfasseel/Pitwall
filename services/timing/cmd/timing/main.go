// Command timing is the Go service skeleton on the bus. It connects to RabbitMQ,
// emits a 1 s control.heartbeat to its own timing.events exchange (Story 1.3),
// logs structured JSON, and shuts down gracefully on SIGTERM. Story 1.4 adds the
// reliability spine: a private SQLite database with a transactional outbox and a
// background relay that publishes durably (sent only after a broker ack) and
// survives a bus outage. Domain logic (laps, sessions, simulator) lands in 1.5+.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/config"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/heartbeat"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/relay"
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

	// Separate confirm-mode channel for the outbox relay (heartbeat stays
	// fire-and-forget on its own channel).
	confirmCh, err := pub.OpenConfirmChannel()
	if err != nil {
		log.Error("failed to open confirm channel for the relay", "error", err.Error())
		_ = pub.Close()
		return 1
	}

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
		Publish:  confirmCh.PublishConfirmed,
		Interval: time.Duration(cfg.OutboxPollInterval) * time.Millisecond,
		Log:      log,
	})

	// Run until SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

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

	<-ctx.Done()
	log.Info("shutdown signal received; draining")

	// Graceful shutdown (NFR19): wait for the heartbeat + relay loops to stop
	// (bounded), make a best-effort final outbox flush, then close cleanly.
	shutdownCtx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.ShutdownTimeout)*time.Millisecond)
	defer cancel()
	waitFor(shutdownCtx, log, "heartbeat loop", loopDone)
	waitFor(shutdownCtx, log, "relay loop", relayDone)

	flushOutbox(shutdownCtx, log, outboxRelay)

	if cerr := confirmCh.Close(); cerr != nil {
		log.Error("error closing relay confirm channel", "error", cerr.Error())
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
