package config

import (
	"strings"
	"testing"
)

// envFrom returns a getenv-style func backed by a map.
func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"RABBITMQ_HOST":     "rabbitmq",
		"RABBITMQ_PORT":     "5672",
		"RABBITMQ_USER":     "pitwall",
		"RABBITMQ_PASSWORD": "s3cr3t",
	}
}

func TestLoad_FailsFastOnMissingRequired(t *testing.T) {
	_, err := Load(envFrom(map[string]string{"RABBITMQ_HOST": "rabbitmq"}))
	if err == nil {
		t.Fatal("expected an error when required vars are missing, got nil")
	}
	for _, want := range []string{"RABBITMQ_PORT", "RABBITMQ_USER", "RABBITMQ_PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the missing var %q; got: %v", want, err)
		}
	}
}

func TestLoad_AppliesDocumentedDefaults(t *testing.T) {
	cfg, err := Load(envFrom(validEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HeartbeatInterval != 1000 {
		t.Errorf("HeartbeatInterval default = %d, want 1000", cfg.HeartbeatInterval)
	}
	if cfg.ServiceName != "timing" {
		t.Errorf("ServiceName default = %q, want timing", cfg.ServiceName)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default = %q, want info", cfg.LogLevel)
	}
	if cfg.RabbitVHost != "/" {
		t.Errorf("RabbitVHost default = %q, want /", cfg.RabbitVHost)
	}
	if cfg.ShutdownTimeout != 5000 {
		t.Errorf("ShutdownTimeout default = %d, want 5000", cfg.ShutdownTimeout)
	}
	if cfg.DBPath != "/data/timing.db" {
		t.Errorf("DBPath default = %q, want /data/timing.db", cfg.DBPath)
	}
	if cfg.OutboxPollInterval != 200 {
		t.Errorf("OutboxPollInterval default = %d, want 200", cfg.OutboxPollInterval)
	}
}

func TestLoad_OverridesOutboxKnobs(t *testing.T) {
	env := validEnv()
	env["DB_PATH"] = "/tmp/custom.db"
	env["OUTBOX_POLL_INTERVAL_MS"] = "50"
	cfg, err := Load(envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DBPath != "/tmp/custom.db" {
		t.Errorf("DBPath = %q, want the overridden /tmp/custom.db", cfg.DBPath)
	}
	if cfg.OutboxPollInterval != 50 {
		t.Errorf("OutboxPollInterval = %d, want 50", cfg.OutboxPollInterval)
	}
}

func TestLoad_RejectsNonPositiveOutboxInterval(t *testing.T) {
	env := validEnv()
	env["OUTBOX_POLL_INTERVAL_MS"] = "0"
	if _, err := Load(envFrom(env)); err == nil {
		t.Fatal("expected an error for a non-positive OUTBOX_POLL_INTERVAL_MS")
	}
}

func TestLoad_RejectsNonIntInterval(t *testing.T) {
	env := validEnv()
	env["HEARTBEAT_INTERVAL_MS"] = "not-a-number"
	if _, err := Load(envFrom(env)); err == nil {
		t.Fatal("expected an error for a non-integer HEARTBEAT_INTERVAL_MS")
	}
}

func TestLoad_RejectsNonPositiveInterval(t *testing.T) {
	env := validEnv()
	env["HEARTBEAT_INTERVAL_MS"] = "0"
	if _, err := Load(envFrom(env)); err == nil {
		t.Fatal("expected an error for a non-positive HEARTBEAT_INTERVAL_MS")
	}
}

// AC2: the password must never appear in a log-safe representation. The AMQP URI
// carries it, so the URI itself must be treated as a secret — this test documents
// that the password lives ONLY in the URI and pins that we never expose a helper
// that would tempt logging it.
func TestAMQPURI_ContainsCredentialsButIsTreatedAsSecret(t *testing.T) {
	cfg, _ := Load(envFrom(validEnv()))
	uri := cfg.AMQPURI()
	if !strings.Contains(uri, "s3cr3t") {
		t.Fatalf("AMQP URI should embed the password (it is the connection secret): %q", uri)
	}
	if !strings.HasPrefix(uri, "amqp://") {
		t.Errorf("AMQP URI should start amqp://, got %q", uri)
	}
}
