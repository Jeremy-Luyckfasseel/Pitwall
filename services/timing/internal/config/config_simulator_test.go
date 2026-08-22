package config

import (
	"strings"
	"testing"
)

// simEnv is a valid base env with the simulator turned on and all required
// simulator knobs present.
func simEnv() map[string]string {
	m := validEnv()
	m["SIMULATOR_ENABLED"] = "true"
	m["SIM_DRIVERS"] = "8"
	m["SIM_LAP_MEAN_MS"] = "45000"
	m["SIM_LAP_STDDEV_MS"] = "2000"
	m["SIM_SESSION_LAPS"] = "10"
	m["MIN_LAP_TIME_MS"] = "10000"
	return m
}

// Default: the simulator is OFF, and none of the SIM_* knobs are required.
func TestLoad_SimulatorDefaultsOffAndRequiresNothing(t *testing.T) {
	cfg, err := Load(envFrom(validEnv()))
	if err != nil {
		t.Fatalf("unexpected error with simulator off: %v", err)
	}
	if cfg.SimulatorEnabled {
		t.Error("SimulatorEnabled should default to false")
	}
}

// AC2: simulator ON but the required knobs unset -> fail fast naming each knob.
func TestLoad_SimulatorOnFailsFastOnMissingKnobs(t *testing.T) {
	env := validEnv()
	env["SIMULATOR_ENABLED"] = "true" // but no SIM_* knobs
	_, err := Load(envFrom(env))
	if err == nil {
		t.Fatal("expected an error when simulator is on but knobs are unset")
	}
	for _, want := range []string{"SIM_DRIVERS", "SIM_LAP_MEAN_MS", "SIM_LAP_STDDEV_MS", "SIM_SESSION_LAPS", "MIN_LAP_TIME_MS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the missing knob %q; got: %v", want, err)
		}
	}
}

// Story 3.5: SIM_SCANNER_OUTAGE_LAPS is optional, defaults to 0 (no outage — no behavior
// change), and must be within [0, SIM_SESSION_LAPS) when set (a window >= the session's laps
// would swallow the whole session — a silent misconfiguration, fail fast naming both knobs).
func TestLoad_SimulatorScannerOutageLapsDefaultsOff(t *testing.T) {
	cfg, err := Load(envFrom(simEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SimScannerOutageLaps != 0 {
		t.Errorf("SimScannerOutageLaps default = %d, want 0", cfg.SimScannerOutageLaps)
	}
}

func TestLoad_SimulatorScannerOutageLapsParsed(t *testing.T) {
	env := simEnv()
	env["SIM_SCANNER_OUTAGE_LAPS"] = "3" // SIM_SESSION_LAPS is 10
	cfg, err := Load(envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SimScannerOutageLaps != 3 {
		t.Errorf("SimScannerOutageLaps = %d, want 3", cfg.SimScannerOutageLaps)
	}
}

func TestLoad_SimulatorRejectsNegativeScannerOutageLaps(t *testing.T) {
	env := simEnv()
	env["SIM_SCANNER_OUTAGE_LAPS"] = "-1"
	if _, err := Load(envFrom(env)); err == nil {
		t.Fatal("expected an error for negative SIM_SCANNER_OUTAGE_LAPS")
	}
}

func TestLoad_SimulatorRejectsScannerOutageLapsAtOrAboveSessionLaps(t *testing.T) {
	env := simEnv() // SIM_SESSION_LAPS = 10
	env["SIM_SCANNER_OUTAGE_LAPS"] = "10"
	_, err := Load(envFrom(env))
	if err == nil {
		t.Fatal("expected an error when SIM_SCANNER_OUTAGE_LAPS >= SIM_SESSION_LAPS")
	}
	for _, want := range []string{"SIM_SCANNER_OUTAGE_LAPS", "SIM_SESSION_LAPS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q; got: %v", want, err)
		}
	}
}

// Simulator ON with all required knobs: parses, applies pacing defaults.
func TestLoad_SimulatorOnParsesKnobsAndDefaults(t *testing.T) {
	cfg, err := Load(envFrom(simEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.SimulatorEnabled {
		t.Fatal("SimulatorEnabled should be true")
	}
	if cfg.SimDrivers != 8 || cfg.SimLapMeanMs != 45000 || cfg.SimLapStddevMs != 2000 || cfg.SimSessionLaps != 10 {
		t.Errorf("parsed knobs = drivers=%d mean=%d stddev=%d laps=%d; want 8/45000/2000/10",
			cfg.SimDrivers, cfg.SimLapMeanMs, cfg.SimLapStddevMs, cfg.SimSessionLaps)
	}
	if cfg.MinLapTimeMs != 10000 {
		t.Errorf("MinLapTimeMs = %d, want 10000", cfg.MinLapTimeMs)
	}
	if cfg.SimTickMs != 250 {
		t.Errorf("SimTickMs default = %d, want 250", cfg.SimTickMs)
	}
	if cfg.SimSessionGapMs != 5000 {
		t.Errorf("SimSessionGapMs default = %d, want 5000", cfg.SimSessionGapMs)
	}
	if cfg.SimSeedSet {
		t.Error("SimSeedSet should be false when SIM_SEED is unset")
	}
}

func TestLoad_SimulatorRejectsNonPositiveDrivers(t *testing.T) {
	env := simEnv()
	env["SIM_DRIVERS"] = "0"
	if _, err := Load(envFrom(env)); err == nil {
		t.Fatal("expected an error for SIM_DRIVERS=0")
	}
}

func TestLoad_SimulatorRejectsBadBool(t *testing.T) {
	env := validEnv()
	env["SIMULATOR_ENABLED"] = "yes-please"
	if _, err := Load(envFrom(env)); err == nil {
		t.Fatal("expected an error for an unparseable SIMULATOR_ENABLED")
	}
}

// AC2/golden-rule guard: a MIN_LAP_TIME_MS at or above the lap-time mean would
// reject most/all simulated laps and empty the demo stream — a misconfiguration
// the loader must catch (naming both knobs).
func TestLoad_SimulatorRejectsMinLapNotBelowMean(t *testing.T) {
	env := simEnv()
	env["MIN_LAP_TIME_MS"] = "45000" // == SIM_LAP_MEAN_MS
	_, err := Load(envFrom(env))
	if err == nil {
		t.Fatal("expected an error when MIN_LAP_TIME_MS >= SIM_LAP_MEAN_MS")
	}
	for _, want := range []string{"MIN_LAP_TIME_MS", "SIM_LAP_MEAN_MS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("guard error should name %q; got: %v", want, err)
		}
	}
}

// AC4 no-regression: MIN_LAP_TIME_MS is inert on the heartbeat-only path — with
// the simulator off it is neither required nor parsed.
func TestLoad_MinLapTimeIgnoredWhenSimulatorOff(t *testing.T) {
	cfg, err := Load(envFrom(validEnv())) // no SIMULATOR_ENABLED, no MIN_LAP_TIME_MS
	if err != nil {
		t.Fatalf("unexpected error with simulator off: %v", err)
	}
	if cfg.MinLapTimeMs != 0 {
		t.Errorf("MinLapTimeMs = %d with simulator off, want 0 (inert)", cfg.MinLapTimeMs)
	}
}

func TestLoad_SimulatorSeedIsOptionalAndTracked(t *testing.T) {
	env := simEnv()
	env["SIM_SEED"] = "42"
	cfg, err := Load(envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.SimSeedSet || cfg.SimSeed != 42 {
		t.Errorf("SimSeed = %d (set=%v), want 42 (set=true)", cfg.SimSeed, cfg.SimSeedSet)
	}
}
