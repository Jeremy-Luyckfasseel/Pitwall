package config

import (
	"strings"
	"testing"
)

// envFunc builds a getenv closure backed by a map (no real process env touched).
func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// baseEnv is a minimal valid environment: just the required broker vars.
func baseEnv() map[string]string {
	return map[string]string{
		"RABBITMQ_HOST":     "rabbitmq",
		"RABBITMQ_PORT":     "5672",
		"RABBITMQ_USER":     "pitwall",
		"RABBITMQ_PASSWORD": "secret",
	}
}

func TestLoad_MissingRequiredVars_FailsFastNamingThem(t *testing.T) {
	_, err := Load(envFunc(map[string]string{}))
	if err == nil {
		t.Fatal("expected an error when required broker vars are missing, got nil")
	}
	for _, want := range []string{"RABBITMQ_HOST", "RABBITMQ_PORT", "RABBITMQ_USER", "RABBITMQ_PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the missing var %q; got: %v", want, err)
		}
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(envFunc(baseEnv()))
	if err != nil {
		t.Fatalf("Load with valid base env should succeed, got: %v", err)
	}
	if cfg.ServiceName != "leaderboard" {
		t.Errorf("ServiceName default = %q, want %q", cfg.ServiceName, "leaderboard")
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr default = %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.DBPath != "/data/leaderboard.db" {
		t.Errorf("DBPath default = %q, want %q", cfg.DBPath, "/data/leaderboard.db")
	}
	if cfg.HeartbeatInterval != 1000 {
		t.Errorf("HeartbeatInterval default = %d, want 1000", cfg.HeartbeatInterval)
	}
	if cfg.ShutdownTimeout != 5000 {
		t.Errorf("ShutdownTimeout default = %d, want 5000", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default = %q, want info", cfg.LogLevel)
	}
	if cfg.LivenessFile != "/tmp/pitwall-leaderboard.live" {
		t.Errorf("LivenessFile default = %q", cfg.LivenessFile)
	}
	if cfg.ConsumePrefetch <= 0 {
		t.Errorf("ConsumePrefetch default = %d, want a positive QoS bound", cfg.ConsumePrefetch)
	}
	if cfg.TimingExchange != "timing.events" {
		t.Errorf("TimingExchange default = %q, want timing.events", cfg.TimingExchange)
	}
}

func TestLoad_OverridesAndAMQPURI(t *testing.T) {
	env := baseEnv()
	env["HTTP_ADDR"] = ":9000"
	env["DB_PATH"] = "/tmp/lb.db"
	env["LOG_LEVEL"] = "debug"
	env["HEARTBEAT_INTERVAL_MS"] = "500"
	env["CONSUME_PREFETCH"] = "32"
	cfg, err := Load(envFunc(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":9000" {
		t.Errorf("HTTPAddr = %q, want :9000", cfg.HTTPAddr)
	}
	if cfg.HeartbeatInterval != 500 {
		t.Errorf("HeartbeatInterval = %d, want 500", cfg.HeartbeatInterval)
	}
	if cfg.ConsumePrefetch != 32 {
		t.Errorf("ConsumePrefetch = %d, want 32", cfg.ConsumePrefetch)
	}
	if got := cfg.AMQPURI(); got != "amqp://pitwall:secret@rabbitmq:5672/" {
		t.Errorf("AMQPURI = %q", got)
	}
}

func TestLoad_RejectsNonIntInterval(t *testing.T) {
	env := baseEnv()
	env["HEARTBEAT_INTERVAL_MS"] = "soon"
	if _, err := Load(envFunc(env)); err == nil {
		t.Fatal("expected an error for non-integer HEARTBEAT_INTERVAL_MS")
	}
}

func TestLoad_RejectsNonPositivePrefetch(t *testing.T) {
	env := baseEnv()
	env["CONSUME_PREFETCH"] = "0"
	if _, err := Load(envFunc(env)); err == nil {
		t.Fatal("expected an error for non-positive CONSUME_PREFETCH")
	}
}
