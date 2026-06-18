package config

import (
	"strings"
	"testing"
)

// The consumer + DLQ knobs (Timing's first inbound consumer, Story 2.3) default to the
// blueprint values (Q&A Round 27) and don't require the simulator.
func TestLoad_ConsumerAndDLQDefaults(t *testing.T) {
	cfg, err := Load(envFrom(validEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ConsumePrefetch != 16 {
		t.Errorf("ConsumePrefetch default = %d, want 16", cfg.ConsumePrefetch)
	}
	if cfg.DLQMaxAttempts != 5 || cfg.DLQRetryBaseMs != 1000 || cfg.DLQRetryMultiplier != 2 || cfg.DLQRetryMaxMs != 60000 {
		t.Errorf("DLQ defaults wrong: %+v", cfg)
	}
}

func TestLoad_DLQMaxMsBelowBaseFailsFast(t *testing.T) {
	env := validEnv()
	env["DLQ_RETRY_BASE_MS"] = "5000"
	env["DLQ_RETRY_MAX_MS"] = "1000"
	if _, err := Load(envFrom(env)); err == nil || !strings.Contains(err.Error(), "DLQ_RETRY_MAX_MS") {
		t.Fatalf("expected a DLQ_RETRY_MAX_MS >= BASE error, got: %v", err)
	}
}

// SIM_TRANSPONDERS defaults to 0 (all QR) and must not exceed SIM_DRIVERS.
func TestLoad_SimTranspondersDefaultZero(t *testing.T) {
	cfg, err := Load(envFrom(simEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SimTransponders != 0 {
		t.Errorf("SimTransponders default = %d, want 0", cfg.SimTransponders)
	}
}

func TestLoad_SimTranspondersWithinDrivers(t *testing.T) {
	env := simEnv()
	env["SIM_DRIVERS"] = "4"
	env["SIM_TRANSPONDERS"] = "2"
	cfg, err := Load(envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SimTransponders != 2 {
		t.Errorf("SimTransponders = %d, want 2", cfg.SimTransponders)
	}
}

func TestLoad_SimTranspondersExceedingDriversFailsFast(t *testing.T) {
	env := simEnv()
	env["SIM_DRIVERS"] = "3"
	env["SIM_TRANSPONDERS"] = "5"
	if _, err := Load(envFrom(env)); err == nil || !strings.Contains(err.Error(), "SIM_TRANSPONDERS") {
		t.Fatalf("expected a SIM_TRANSPONDERS <= SIM_DRIVERS error, got: %v", err)
	}
}
