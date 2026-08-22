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

	// Simulator (Story 1.5). The toggle defaults OFF; when ON, the data-shaping
	// knobs are REQUIRED (fail fast, never assumed — AC2). The pacing knobs are
	// correctness-neutral and may default.
	SimulatorEnabled bool
	SimDrivers       int   // N drivers; required when on, >= 1
	SimLapMeanMs     int   // normal-distribution mean lap time; required when on, >= 1
	SimLapStddevMs   int   // normal-distribution stddev; required when on, >= 0
	SimSessionLaps   int   // counted laps per driver before the session ends; required when on, >= 1
	SimTickMs        int   // wall-clock pacing between emission rounds; optional, default 250
	SimSessionGapMs  int   // pause between sessions in the continuous loop; optional, default 5000
	SimSeed          int64 // optional RNG seed; deterministic when set
	SimSeedSet       bool  // whether SIM_SEED was provided

	// MinLapTimeMs is the lap-validity rule (FR35, Story 1.6): a crossing closer
	// than this to the previous valid one is rejected as a bounce/duplicate. It is
	// REQUIRED when the simulator is on (the only crossing source in Epic 1) and
	// inert (0) when off — so the heartbeat-only path gains no new required var.
	MinLapTimeMs int

	// SimTransponders is how many of the N simulator drivers check in via a transponder
	// (the rest via QR). Optional, default 0 (all QR); must be <= SimDrivers. Exercises
	// the transponder->masterId map resolution path (Story 2.3, AC2).
	SimTransponders int

	// UnknownTokenScans is how many synthetic "stray" (unbound-transponder) line
	// crossings the simulator injects per session — the register-first/unknown-token
	// operator exception (Story 2.5, FR39). Optional, default 0 (no behavior change);
	// must be >= 0.
	UnknownTokenScans int

	// SimScannerOutageLaps is how many consecutive start-finish crossings the simulator
	// drops per session to model a scanner outage — the scanner-offline/persist-first path
	// (Story 3.5, FR38). Optional, default 0 (no outage — no behavior change); must be
	// within [0, SimSessionLaps) (a window at/above the session's laps would swallow the
	// whole session — a silent misconfiguration).
	SimScannerOutageLaps int

	// Consumer + DLQ knobs — Timing's FIRST inbound consumer (identity.resolved) arrives
	// in Story 2.3. ConsumePrefetch bounds in-flight unacked deliveries (QoS, > 0). The
	// DLQ policy (Story 1.9; values pinned Q&A Round 27) governs retry-then-park.
	ConsumePrefetch    int
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
	pollInterval, err := intEnv(getenv, "OUTBOX_POLL_INTERVAL_MS", 200)
	if err != nil {
		return nil, err
	}
	if pollInterval <= 0 {
		return nil, fmt.Errorf("OUTBOX_POLL_INTERVAL_MS must be a positive integer, got %d", pollInterval)
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
		OutboxPollInterval: pollInterval,
		LogLevel:           firstNonEmpty(getenv("LOG_LEVEL"), "info"),
		ServiceName:        firstNonEmpty(getenv("SERVICE_NAME"), "timing"),
		LivenessFile:       firstNonEmpty(getenv("LIVENESS_FILE"), "/tmp/pitwall-timing.live"),
		DBPath:             firstNonEmpty(getenv("DB_PATH"), "/data/timing.db"),
		InstanceID:         getenv("INSTANCE_ID"),
		ContractDir:        getenv("CONTRACT_DIR"),
		ConsumePrefetch:    prefetch,
		DLQMaxAttempts:     dlqMaxAttempts,
		DLQRetryBaseMs:     dlqBaseMs,
		DLQRetryMultiplier: dlqMultiplier,
		DLQRetryMaxMs:      dlqMaxMs,
	}

	if err := loadSimulator(getenv, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadSimulator parses the simulator toggle and, when it is on, the required
// data-shaping knobs (golden rule: fail fast naming every missing/invalid knob,
// never assume — AC2). Pacing knobs are correctness-neutral and default.
func loadSimulator(getenv func(string) string, cfg *Config) error {
	enabled, err := boolEnv(getenv, "SIMULATOR_ENABLED", false)
	if err != nil {
		return err
	}
	cfg.SimulatorEnabled = enabled
	if !enabled {
		return nil
	}

	// Required, data-shaping knobs — collect ALL problems so one run reports them all.
	var problems []string
	cfg.SimDrivers = requirePositive(getenv, "SIM_DRIVERS", &problems)
	cfg.SimLapMeanMs = requirePositive(getenv, "SIM_LAP_MEAN_MS", &problems)
	cfg.SimLapStddevMs = requireNonNegative(getenv, "SIM_LAP_STDDEV_MS", &problems)
	cfg.SimSessionLaps = requirePositive(getenv, "SIM_SESSION_LAPS", &problems)
	// MIN_LAP_TIME_MS is the lap-validity rule (FR35); required when the simulator
	// is on (it is the only crossing source in Epic 1).
	cfg.MinLapTimeMs = requirePositive(getenv, "MIN_LAP_TIME_MS", &problems)
	if len(problems) > 0 {
		return fmt.Errorf("simulator is enabled but its config is invalid: %s", strings.Join(problems, "; "))
	}

	// Guard: a min-lap-time at or above the lap-time mean would reject most/all
	// simulated laps and empty the demo stream — a silent misconfiguration. Fail
	// fast naming both knobs (golden rule). (Only checked once both parsed clean.)
	if cfg.MinLapTimeMs >= cfg.SimLapMeanMs {
		return fmt.Errorf("MIN_LAP_TIME_MS (%d) must be below SIM_LAP_MEAN_MS (%d), else the simulator would reject most laps",
			cfg.MinLapTimeMs, cfg.SimLapMeanMs)
	}

	// SIM_TRANSPONDERS: how many drivers check in via a transponder (rest QR). Optional,
	// default 0; must be within [0, SIM_DRIVERS] (you cannot have more transponder
	// drivers than drivers — a silent misconfiguration otherwise).
	transponders, err := intEnv(getenv, "SIM_TRANSPONDERS", 0)
	if err != nil {
		return err
	}
	if transponders < 0 || transponders > cfg.SimDrivers {
		return fmt.Errorf("SIM_TRANSPONDERS (%d) must be between 0 and SIM_DRIVERS (%d)", transponders, cfg.SimDrivers)
	}
	cfg.SimTransponders = transponders

	// SIM_UNKNOWN_TOKEN_SCANS (Story 2.5): optional, default 0; must be >= 0 (not tied
	// to SimDrivers — it is an independent stray-scan count, not a driver count).
	unknownTokenScans, err := intEnv(getenv, "SIM_UNKNOWN_TOKEN_SCANS", 0)
	if err != nil {
		return err
	}
	if unknownTokenScans < 0 {
		return fmt.Errorf("SIM_UNKNOWN_TOKEN_SCANS must be >= 0, got %d", unknownTokenScans)
	}
	cfg.UnknownTokenScans = unknownTokenScans

	// SIM_SCANNER_OUTAGE_LAPS (Story 3.5): optional, default 0 (no outage). Must be >= 0 and,
	// when set, strictly below SimSessionLaps so laps remain before and after the gap — else
	// the window would swallow the whole session (a silent misconfiguration). Fail fast
	// naming both knobs (golden rule), mirroring the MIN_LAP_TIME_MS >= SIM_LAP_MEAN_MS guard.
	outageLaps, err := intEnv(getenv, "SIM_SCANNER_OUTAGE_LAPS", 0)
	if err != nil {
		return err
	}
	if outageLaps < 0 {
		return fmt.Errorf("SIM_SCANNER_OUTAGE_LAPS must be >= 0, got %d", outageLaps)
	}
	if outageLaps >= cfg.SimSessionLaps {
		return fmt.Errorf("SIM_SCANNER_OUTAGE_LAPS (%d) must be below SIM_SESSION_LAPS (%d), else the outage would swallow the whole session",
			outageLaps, cfg.SimSessionLaps)
	}
	cfg.SimScannerOutageLaps = outageLaps

	// Optional, correctness-neutral pacing knobs (defaults allowed).
	tick, err := intEnv(getenv, "SIM_TICK_MS", 250)
	if err != nil {
		return err
	}
	if tick < 0 {
		return fmt.Errorf("SIM_TICK_MS must be >= 0, got %d", tick)
	}
	cfg.SimTickMs = tick

	gap, err := intEnv(getenv, "SIM_SESSION_GAP_MS", 5000)
	if err != nil {
		return err
	}
	if gap < 0 {
		return fmt.Errorf("SIM_SESSION_GAP_MS must be >= 0, got %d", gap)
	}
	cfg.SimSessionGapMs = gap

	if raw := strings.TrimSpace(getenv("SIM_SEED")); raw != "" {
		seed, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil {
			return fmt.Errorf("SIM_SEED must be an integer, got %q", raw)
		}
		cfg.SimSeed = seed
		cfg.SimSeedSet = true
	}
	return nil
}

// requirePositive reads an int env that must be present and >= 1; otherwise it
// appends a clear problem string (and returns 0). It never assumes a default.
func requirePositive(getenv func(string) string, key string, problems *[]string) int {
	return requireBounded(getenv, key, 1, problems)
}

// requireNonNegative reads an int env that must be present and >= 0.
func requireNonNegative(getenv func(string) string, key string, problems *[]string) int {
	return requireBounded(getenv, key, 0, problems)
}

func requireBounded(getenv func(string) string, key string, min int, problems *[]string) int {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		*problems = append(*problems, key+" is required when the simulator is enabled")
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s must be an integer, got %q", key, raw))
		return 0
	}
	if n < min {
		*problems = append(*problems, fmt.Sprintf("%s must be >= %d, got %d", key, min, n))
		return 0
	}
	return n
}

// boolEnv parses a strict boolean (true/false/1/0, case-insensitive). An empty
// value yields def; anything else is an error (never silently coerced).
func boolEnv(getenv func(string) string, key string, def bool) (bool, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return def, nil
	}
	switch strings.ToLower(raw) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean (true/false), got %q", key, raw)
	}
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
