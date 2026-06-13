//go:build integration

package conformance

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// repoRoot resolves the monorepo root from this test file's location
// (<root>/tests/conformance/go/...).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller for repo root")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	return root
}

// Built binaries are cached per service for the whole test binary run (building
// is ~seconds; scenarios share the artifacts).
var (
	buildDir  string
	buildOnce sync.Once
	binCache  = map[string]string{}
	binMu     sync.Mutex
)

func buildBinary(t *testing.T, service string) string {
	t.Helper()
	buildOnce.Do(func() {
		d, err := os.MkdirTemp("", "pitwall-conformance-bin-")
		if err != nil {
			t.Fatalf("mkdir build dir: %v", err)
		}
		buildDir = d
	})
	binMu.Lock()
	defer binMu.Unlock()
	if p, ok := binCache[service]; ok {
		return p
	}
	out := filepath.Join(buildDir, exeName(service))
	cmd := exec.Command("go", "build", "-o", out, "./cmd/"+service)
	cmd.Dir = filepath.Join(repoRoot(t), "services", service)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", service, err, outBytes)
	}
	binCache[service] = out
	return out
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// svcProc is a running real service binary. It captures stdout (for structured
// log markers) and can be killed + restarted with identical env (crash-after-ack).
type svcProc struct {
	name    string
	exe     string
	env     []string
	baseURL string // leaderboard HTTP base; "" for timing
	cmd     *exec.Cmd
	out     *syncBuf
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (p *svcProc) launch(t *testing.T) {
	t.Helper()
	cmd := exec.Command(p.exe)
	cmd.Env = p.env
	cmd.Stdout = p.out
	cmd.Stderr = p.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", p.name, err)
	}
	p.cmd = cmd
}

// kill hard-stops the process (used both for teardown and to simulate a crash).
func (p *svcProc) kill(t *testing.T) {
	t.Helper()
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	_, _ = p.cmd.Process.Wait()
	p.cmd = nil
}

// restart simulates a process crash + relaunch on the SAME database/port.
func (p *svcProc) restart(t *testing.T) {
	t.Helper()
	p.kill(t)
	p.launch(t)
	if p.baseURL != "" {
		p.waitReady(t)
	}
}

// waitReady blocks until the Leaderboard HTTP/SSE endpoint serves a snapshot.
func (p *svcProc) waitReady(t *testing.T) {
	t.Helper()
	waitUntil(t, p.name+" HTTP ready", 30*time.Second, func() bool {
		_, err := fetchSnapshot(p.baseURL)
		return err == nil
	})
}

// snapshot fetches the current served board (fails the test on error). Use only
// for one-shot post-wait assertions, never inside a waitUntil poll.
func (p *svcProc) snapshot(t *testing.T) snapshotView {
	t.Helper()
	snap, err := fetchSnapshot(p.baseURL)
	if err != nil {
		t.Fatalf("snapshot %s: %v", p.name, err)
	}
	return snap
}

// trySnapshot fetches the served board WITHOUT failing the test — a transient SSE
// read error returns (zero, err) so a waitUntil poll treats it as "not ready yet"
// instead of killing the run on a momentary blip.
func (p *svcProc) trySnapshot() (snapshotView, error) {
	return fetchSnapshot(p.baseURL)
}

// mergeEnv overlays overrides onto the current environment (overrides win).
func mergeEnv(overrides map[string]string) []string {
	keep := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		k := kv[:strings.IndexByte(kv, '=')]
		if _, override := overrides[k]; !override {
			keep = append(keep, kv)
		}
	}
	for k, v := range overrides {
		keep = append(keep, k+"="+v)
	}
	return keep
}

// rabbitEnv splits an amqp://user:pass@host:port/ URL into the service's
// RABBITMQ_* env vars.
func rabbitEnv(amqpURL string) map[string]string {
	// amqp://guest:guest@localhost:PORT/
	rest := strings.TrimPrefix(amqpURL, "amqp://")
	creds, hostpart, _ := strings.Cut(rest, "@")
	user, pass, _ := strings.Cut(creds, ":")
	hostport := strings.TrimSuffix(hostpart, "/")
	host, port, _ := strings.Cut(hostport, ":")
	return map[string]string{
		"RABBITMQ_HOST":     host,
		"RABBITMQ_PORT":     port,
		"RABBITMQ_USER":     user,
		"RABBITMQ_PASSWORD": pass,
	}
}

// startLeaderboard builds + runs the REAL Leaderboard binary against amqpURL,
// returning a handle once its SSE endpoint is serving. The DB path + HTTP port
// are stable across restart (crash-after-ack).
func startLeaderboard(t *testing.T, amqpURL string) *svcProc {
	t.Helper()
	exe := buildBinary(t, "leaderboard")
	httpPort := freeTCPPort(t)
	dbPath := filepath.Join(t.TempDir(), "leaderboard.db")
	live := filepath.Join(t.TempDir(), "leaderboard.live")
	env := map[string]string{
		"HTTP_ADDR":     ":" + httpPort,
		"DB_PATH":       dbPath,
		"CONTRACT_DIR":  filepath.Join(repoRoot(t), "contract"),
		"LIVENESS_FILE": live,
		"SERVICE_NAME":  "leaderboard",
		"LOG_LEVEL":     "info",
	}
	for k, v := range rabbitEnv(amqpURL) {
		env[k] = v
	}
	p := &svcProc{
		name:    "leaderboard",
		exe:     exe,
		env:     mergeEnv(env),
		baseURL: fmt.Sprintf("http://localhost:%s", httpPort),
		out:     &syncBuf{},
	}
	p.launch(t)
	t.Cleanup(func() { p.kill(t) })
	p.waitReady(t)
	return p
}

// startTimingSimulator builds + runs the REAL Timing binary in simulator mode
// (seed-deterministic) as the lap producer. One session only within the test
// window (a long inter-session gap defers any second session).
func startTimingSimulator(t *testing.T, amqpURL string, sim SimulatorSpec) *svcProc {
	t.Helper()
	exe := buildBinary(t, "timing")
	dbPath := filepath.Join(t.TempDir(), "timing.db")
	live := filepath.Join(t.TempDir(), "timing.live")
	tickMs := sim.TickMs
	if tickMs <= 0 {
		tickMs = 10 // fast pacing by default
	}
	env := map[string]string{
		"SIMULATOR_ENABLED":  "true",
		"SIM_DRIVERS":        strconv.Itoa(sim.Drivers),
		"SIM_SESSION_LAPS":   strconv.Itoa(sim.SessionLaps),
		"SIM_SEED":           strconv.FormatInt(sim.Seed, 10),
		"SIM_LAP_MEAN_MS":    "41000",
		"SIM_LAP_STDDEV_MS":  "600",
		"MIN_LAP_TIME_MS":    "1000",
		"SIM_TICK_MS":        strconv.Itoa(tickMs),
		"SIM_SESSION_GAP_MS": "600000", // defer the next session past the test window
		"DB_PATH":            dbPath,
		"CONTRACT_DIR":       filepath.Join(repoRoot(t), "contract"),
		"LIVENESS_FILE":      live,
		"SERVICE_NAME":       "timing",
		"LOG_LEVEL":          "info",
	}
	for k, v := range rabbitEnv(amqpURL) {
		env[k] = v
	}
	p := &svcProc{
		name: "timing",
		exe:  exe,
		env:  mergeEnv(env),
		out:  &syncBuf{},
	}
	p.launch(t)
	t.Cleanup(func() { p.kill(t) })
	return p
}
