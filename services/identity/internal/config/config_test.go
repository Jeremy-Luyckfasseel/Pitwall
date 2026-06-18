package config_test

import (
	"strings"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/config"
)

// envMap returns a getenv func backed by m (nil -> empty), for deterministic tests.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func requiredEnv() map[string]string {
	return map[string]string{
		"RABBITMQ_HOST":     "rabbitmq",
		"RABBITMQ_PORT":     "5672",
		"RABBITMQ_USER":     "pitwall",
		"RABBITMQ_PASSWORD": "secret",
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	cfg, err := config.Load(envMap(requiredEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServiceName != "identity" {
		t.Errorf("ServiceName = %q; want identity", cfg.ServiceName)
	}
	if cfg.SourceExchange != "frontend.events" {
		t.Errorf("SourceExchange = %q; want frontend.events (the originating intent exchange)", cfg.SourceExchange)
	}
	if cfg.DBPath != "/data/identity.db" {
		t.Errorf("DBPath = %q; want /data/identity.db", cfg.DBPath)
	}
	if cfg.LivenessFile != "/tmp/pitwall-identity.live" {
		t.Errorf("LivenessFile = %q; want /tmp/pitwall-identity.live", cfg.LivenessFile)
	}
	if cfg.HeartbeatInterval != 1000 {
		t.Errorf("HeartbeatInterval = %d; want 1000", cfg.HeartbeatInterval)
	}
	if cfg.OutboxPollInterval != 200 {
		t.Errorf("OutboxPollInterval = %d; want 200", cfg.OutboxPollInterval)
	}
	if cfg.ConsumePrefetch != 16 {
		t.Errorf("ConsumePrefetch = %d; want 16", cfg.ConsumePrefetch)
	}
	if cfg.DLQMaxAttempts != 5 || cfg.DLQRetryBaseMs != 1000 || cfg.DLQRetryMultiplier != 2 || cfg.DLQRetryMaxMs != 60000 {
		t.Errorf("DLQ defaults = %d/%d/%d/%d; want 5/1000/2/60000",
			cfg.DLQMaxAttempts, cfg.DLQRetryBaseMs, cfg.DLQRetryMultiplier, cfg.DLQRetryMaxMs)
	}
}

func TestLoad_MissingRequiredFailsFast(t *testing.T) {
	_, err := config.Load(envMap(map[string]string{"RABBITMQ_HOST": "rabbitmq"}))
	if err == nil {
		t.Fatal("Load with missing required vars returned nil error; want failure")
	}
	for _, want := range []string{"RABBITMQ_PORT", "RABBITMQ_USER", "RABBITMQ_PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should list missing var %q", err.Error(), want)
		}
	}
}

func TestLoad_RejectsNonIntHeartbeat(t *testing.T) {
	env := requiredEnv()
	env["HEARTBEAT_INTERVAL_MS"] = "not-a-number"
	if _, err := config.Load(envMap(env)); err == nil {
		t.Fatal("non-integer HEARTBEAT_INTERVAL_MS should fail")
	}
}

// SHUTDOWN_TIMEOUT_MS must fail fast on a non-positive value, like every other int
// knob — a 0/negative timeout yields an already-expired DB-open + shutdown-flush context.
func TestLoad_RejectsNonPositiveShutdownTimeout(t *testing.T) {
	for _, bad := range []string{"0", "-1"} {
		env := requiredEnv()
		env["SHUTDOWN_TIMEOUT_MS"] = bad
		if _, err := config.Load(envMap(env)); err == nil {
			t.Fatalf("SHUTDOWN_TIMEOUT_MS=%q should fail fast; got nil error", bad)
		}
	}
}

func TestLoad_HonoursOverrides(t *testing.T) {
	env := requiredEnv()
	env["SERVICE_NAME"] = "identity-2"
	env["SOURCE_EXCHANGE"] = "bar.events"
	env["OUTBOX_POLL_INTERVAL_MS"] = "50"
	cfg, err := config.Load(envMap(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServiceName != "identity-2" || cfg.SourceExchange != "bar.events" || cfg.OutboxPollInterval != 50 {
		t.Errorf("overrides not honoured: %+v", cfg)
	}
}

func TestAMQPURI_EmbedsCredentials(t *testing.T) {
	cfg, err := config.Load(envMap(requiredEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.AMQPURI()
	if got != "amqp://pitwall:secret@rabbitmq:5672/" {
		t.Errorf("AMQPURI = %q; want amqp://pitwall:secret@rabbitmq:5672/", got)
	}
}
