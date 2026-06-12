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

// TestLoad_DLQDefaults pins the Story-1.9 / Q&A-Round-27 DLQ knobs: 5 attempts,
// 1 s base, ×2 per hop, 60 s ceiling. These are confirm-at-build values — the
// defaults must match exactly what the user approved.
func TestLoad_DLQDefaults(t *testing.T) {
	cfg, err := Load(envFunc(baseEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DLQMaxAttempts != 5 {
		t.Errorf("DLQMaxAttempts default = %d, want 5", cfg.DLQMaxAttempts)
	}
	if cfg.DLQRetryBaseMs != 1000 {
		t.Errorf("DLQRetryBaseMs default = %d, want 1000", cfg.DLQRetryBaseMs)
	}
	if cfg.DLQRetryMultiplier != 2 {
		t.Errorf("DLQRetryMultiplier default = %d, want 2", cfg.DLQRetryMultiplier)
	}
	if cfg.DLQRetryMaxMs != 60000 {
		t.Errorf("DLQRetryMaxMs default = %d, want 60000", cfg.DLQRetryMaxMs)
	}
}

func TestLoad_DLQOverrides(t *testing.T) {
	env := baseEnv()
	env["DLQ_MAX_ATTEMPTS"] = "3"
	env["DLQ_RETRY_BASE_MS"] = "500"
	env["DLQ_RETRY_MULTIPLIER"] = "3"
	env["DLQ_RETRY_MAX_MS"] = "30000"
	cfg, err := Load(envFunc(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DLQMaxAttempts != 3 || cfg.DLQRetryBaseMs != 500 ||
		cfg.DLQRetryMultiplier != 3 || cfg.DLQRetryMaxMs != 30000 {
		t.Errorf("DLQ overrides not applied: %+v", cfg)
	}
}

func TestLoad_RejectsBadDLQKnobs(t *testing.T) {
	cases := map[string]map[string]string{
		"DLQ_MAX_ATTEMPTS < 1":     {"DLQ_MAX_ATTEMPTS": "0"},
		"DLQ_RETRY_BASE_MS <= 0":   {"DLQ_RETRY_BASE_MS": "0"},
		"DLQ_RETRY_MULTIPLIER < 1": {"DLQ_RETRY_MULTIPLIER": "0"},
		"DLQ_RETRY_MAX_MS < base":  {"DLQ_RETRY_BASE_MS": "5000", "DLQ_RETRY_MAX_MS": "1000"},
		"DLQ_MAX_ATTEMPTS non-int": {"DLQ_MAX_ATTEMPTS": "lots"},
	}
	for name, overrides := range cases {
		env := baseEnv()
		for k, v := range overrides {
			env[k] = v
		}
		if _, err := Load(envFunc(env)); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}
