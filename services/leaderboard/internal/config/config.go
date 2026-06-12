// Package config loads and validates the Leaderboard service configuration from
// the environment (12-factor; blueprint §config). Required values fail fast with
// a clear message — the service never assumes a value (golden rule). The pattern
// mirrors services/timing/internal/config; the libs/go-pitwall extraction that
// would DRY the two is Story 2.1, not now.
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

	HeartbeatInterval int // milliseconds
	ShutdownTimeout   int // milliseconds
	LogLevel          string
	ServiceName       string
	LivenessFile      string
	DBPath            string // private SQLite database (inbox + standings projection)
	InstanceID        string // optional; minted at startup if empty
	ContractDir       string // optional; resolved by the validator if empty

	// HTTPAddr is the listen address for the SSE endpoint + the //go:embed-ed
	// trackside SPA (Q26.1: live UI is SSE, never HTTP polling, never an HTTP
	// /health). Operationally-neutral, so a documented default is allowed.
	HTTPAddr string

	// TimingExchange is the PRODUCER's exchange this consumer binds its queue to
	// (a service consumes from the producer's exchange and publishes to its own).
	TimingExchange string

	// ConsumePrefetch bounds in-flight unacked deliveries (QoS). Must be > 0.
	ConsumePrefetch int

	// DLQ poison-message policy (Story 1.9; values pinned in Q&A Round 27 —
	// confirm-at-build, never invented). DLQMaxAttempts is the total processing
	// attempts before terminal parking; the per-hop retry delay grows
	// exponentially from DLQRetryBaseMs by DLQRetryMultiplier, capped at
	// DLQRetryMaxMs. Defaults: 5 attempts · 1 s base · ×2 · 60 s ceiling
	// (retry hops 1 s → 2 s → 4 s → 8 s, then park).
	DLQMaxAttempts     int
	DLQRetryBaseMs     int
	DLQRetryMultiplier int
	DLQRetryMaxMs      int
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
	prefetch, err := intEnv(getenv, "CONSUME_PREFETCH", 16)
	if err != nil {
		return nil, err
	}
	if prefetch <= 0 {
		return nil, fmt.Errorf("CONSUME_PREFETCH must be a positive integer, got %d", prefetch)
	}

	dlqMaxAttempts, err := intEnv(getenv, "DLQ_MAX_ATTEMPTS", 5)
	if err != nil {
		return nil, err
	}
	if dlqMaxAttempts < 1 {
		return nil, fmt.Errorf("DLQ_MAX_ATTEMPTS must be >= 1, got %d", dlqMaxAttempts)
	}
	dlqBaseMs, err := intEnv(getenv, "DLQ_RETRY_BASE_MS", 1000)
	if err != nil {
		return nil, err
	}
	if dlqBaseMs <= 0 {
		return nil, fmt.Errorf("DLQ_RETRY_BASE_MS must be a positive integer, got %d", dlqBaseMs)
	}
	dlqMultiplier, err := intEnv(getenv, "DLQ_RETRY_MULTIPLIER", 2)
	if err != nil {
		return nil, err
	}
	if dlqMultiplier < 1 {
		return nil, fmt.Errorf("DLQ_RETRY_MULTIPLIER must be >= 1, got %d", dlqMultiplier)
	}
	dlqMaxMs, err := intEnv(getenv, "DLQ_RETRY_MAX_MS", 60000)
	if err != nil {
		return nil, err
	}
	if dlqMaxMs < dlqBaseMs {
		return nil, fmt.Errorf("DLQ_RETRY_MAX_MS (%d) must be >= DLQ_RETRY_BASE_MS (%d)", dlqMaxMs, dlqBaseMs)
	}

	cfg := &Config{
		RabbitHost:         getenv("RABBITMQ_HOST"),
		RabbitPort:         getenv("RABBITMQ_PORT"),
		RabbitUser:         getenv("RABBITMQ_USER"),
		RabbitPassword:     getenv("RABBITMQ_PASSWORD"),
		RabbitVHost:        firstNonEmpty(getenv("RABBITMQ_VHOST"), "/"),
		HeartbeatInterval:  interval,
		ShutdownTimeout:    shutdown,
		LogLevel:           firstNonEmpty(getenv("LOG_LEVEL"), "info"),
		ServiceName:        firstNonEmpty(getenv("SERVICE_NAME"), "leaderboard"),
		LivenessFile:       firstNonEmpty(getenv("LIVENESS_FILE"), "/tmp/pitwall-leaderboard.live"),
		DBPath:             firstNonEmpty(getenv("DB_PATH"), "/data/leaderboard.db"),
		InstanceID:         getenv("INSTANCE_ID"),
		ContractDir:        getenv("CONTRACT_DIR"),
		HTTPAddr:           firstNonEmpty(getenv("HTTP_ADDR"), ":8080"),
		TimingExchange:     firstNonEmpty(getenv("TIMING_EXCHANGE"), "timing.events"),
		ConsumePrefetch:    prefetch,
		DLQMaxAttempts:     dlqMaxAttempts,
		DLQRetryBaseMs:     dlqBaseMs,
		DLQRetryMultiplier: dlqMultiplier,
		DLQRetryMaxMs:      dlqMaxMs,
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
