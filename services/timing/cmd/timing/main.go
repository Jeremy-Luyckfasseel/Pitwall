// Command timing is the Go service skeleton on the bus (Story 1.3): it connects
// to RabbitMQ, emits a 1 s control.heartbeat to its own timing.events exchange,
// logs structured JSON, and shuts down gracefully on SIGTERM. Domain logic
// (laps, sessions, simulator) lands in Stories 1.5+.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/config"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/heartbeat"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
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

	// Run until SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	loopDone := make(chan struct{})
	go func() {
		_ = emitter.Run(ctx)
		close(loopDone)
	}()

	<-ctx.Done()
	log.Info("shutdown signal received; draining")

	// Graceful shutdown (NFR19): wait for the in-flight publish / loop to finish
	// (bounded), flush the outbox, then close the channel + connection cleanly.
	shutdownCtx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.ShutdownTimeout)*time.Millisecond)
	defer cancel()
	select {
	case <-loopDone:
	case <-shutdownCtx.Done():
		log.Warn("heartbeat loop did not stop within the shutdown timeout")
	}

	flushOutbox(log)

	if err := pub.Close(); err != nil {
		log.Error("error closing broker connection", "error", err.Error())
		return 1
	}
	log.Info("shutdown complete")
	return 0
}

// flushOutbox is the seam for the transactional outbox flush. The outbox itself
// lands in Story 1.4; today this is a no-op so the graceful-shutdown sequence is
// already in the right shape (blueprint §Liveness: "flushes the outbox where possible").
func flushOutbox(log interface{ Info(string, ...any) }) {
	log.Info("outbox flush: no-op (transactional outbox arrives in Story 1.4)")
}
