// Package config loads and validates the Timing service configuration from the
// environment (12-factor; blueprint §config). Required values fail fast with a
// clear message — the service never assumes a value (golden rule).
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Config is the fully-resolved, validated service configuration.
type Config struct {
	RabbitHost     string
	RabbitPort     string
	RabbitUser     string
	RabbitPassword string
	RabbitVHost    string

	HeartbeatInterval  int // milliseconds
	ShutdownTimeout    int // milliseconds
	OutboxPollInterval int // milliseconds; relay poll backstop (Story 1.4)
	LogLevel           string
	ServiceName        string
	LivenessFile       string
	DBPath             string // private SQLite database (Story 1.4)
	InstanceID         string // optional; minted at startup if empty
	ContractDir        string // optional; resolved by the validator if empty
}

// requiredVars must be present and non-empty.
var requiredVars = []string{"RABBITMQ_HOST", "RABBITMQ_PORT", "RABBITMQ_USER", "RABBITMQ_PASSWORD"}

// Load resolves the configuration using the supplied getenv function (inject
// os.Getenv in production; a map-backed func in tests). It returns an error
// listing every missing required variable rather than assuming defaults for them.
func Load(getenv func(string) string) (*Config, error) {
	var missing []string
	for _, k := range requiredVars {
		if strings.TrimSpace(getenv(k)) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	interval, err := intEnv(getenv, "HEARTBEAT_INTERVAL_MS", 1000)
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		return nil, fmt.Errorf("HEARTBEAT_INTERVAL_MS must be a positive integer, got %d", interval)
	}
	shutdown, err := intEnv(getenv, "SHUTDOWN_TIMEOUT_MS", 5000)
	if err != nil {
		return nil, err
	}
	pollInterval, err := intEnv(getenv, "OUTBOX_POLL_INTERVAL_MS", 200)
	if err != nil {
		return nil, err
	}
	if pollInterval <= 0 {
		return nil, fmt.Errorf("OUTBOX_POLL_INTERVAL_MS must be a positive integer, got %d", pollInterval)
	}

	cfg := &Config{
		RabbitHost:         getenv("RABBITMQ_HOST"),
		RabbitPort:         getenv("RABBITMQ_PORT"),
		RabbitUser:         getenv("RABBITMQ_USER"),
		RabbitPassword:     getenv("RABBITMQ_PASSWORD"),
		RabbitVHost:        firstNonEmpty(getenv("RABBITMQ_VHOST"), "/"),
		HeartbeatInterval:  interval,
		ShutdownTimeout:    shutdown,
		OutboxPollInterval: pollInterval,
		LogLevel:           firstNonEmpty(getenv("LOG_LEVEL"), "info"),
		ServiceName:        firstNonEmpty(getenv("SERVICE_NAME"), "timing"),
		LivenessFile:       firstNonEmpty(getenv("LIVENESS_FILE"), "/tmp/pitwall-timing.live"),
		DBPath:             firstNonEmpty(getenv("DB_PATH"), "/data/timing.db"),
		InstanceID:         getenv("INSTANCE_ID"),
		ContractDir:        getenv("CONTRACT_DIR"),
	}
	return cfg, nil
}

// AMQPURI builds the broker connection string. It is intentionally NOT logged:
// it embeds the password. Credentials are URL-encoded so special characters are safe.
func (c *Config) AMQPURI() string {
	u := url.URL{
		Scheme: "amqp",
		User:   url.UserPassword(c.RabbitUser, c.RabbitPassword),
		Host:   c.RabbitHost + ":" + c.RabbitPort,
		Path:   c.RabbitVHost,
	}
	return u.String()
}

func intEnv(getenv func(string) string, key string, def int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, raw)
	}
	return n, nil
}

func firstNonEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
