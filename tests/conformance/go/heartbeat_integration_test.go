//go:build integration

package conformance

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// wireTimePattern mirrors the /contract pattern every timestamp field is pinned to
// (exactly 3-digit milliseconds, literal 'Z' — the exact class of bug Task 1/3's
// codegen fixes protect against).
var wireTimePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// runHeartbeat is the Go side of Story 3.1's cross-language skeleton-mechanics
// scenario (AC4): starts the REAL Timing binary with its simulator disabled (so it
// is exercising nothing but the shared blueprint skeleton — connect, declare its own
// exchange, emit control.heartbeat, graceful shutdown), observes the RAW bus traffic
// directly (never Leaderboard's board — Timing's heartbeat is not a domain event),
// and asserts SIGTERM produces a clean exit. A sibling Python runner
// (tests/conformance/python) implements the SAME scenario against Driver.
func runHeartbeat(t *testing.T, sc Scenario) {
	if sc.Heartbeat == nil {
		t.Fatal("heartbeat scenario missing its `heartbeat:` spec block")
	}
	br := startBroker(t)
	p := startTimingHeartbeatOnly(t, br.amqpURL, sc.Heartbeat.IntervalMs)

	beats := observeHeartbeats(t, br.amqpURL, "timing.events", "timing", sc.Heartbeat.MinCount,
		time.Duration(sc.Heartbeat.WindowMs)*time.Millisecond)
	if len(beats) < sc.Heartbeat.MinCount {
		t.Fatalf("observed %d heartbeats in %dms, want >= %d", len(beats), sc.Heartbeat.WindowMs, sc.Heartbeat.MinCount)
	}
	for _, b := range beats {
		assertHeartbeatShape(t, b, "timing")
	}
	// Successive beats must carry a fresh `at` (proves the loop is actually ticking,
	// not replaying/caching one stamped envelope).
	seenAt := map[string]bool{}
	for _, b := range beats {
		seenAt[b.Data.At] = true
	}
	if len(seenAt) < 2 {
		t.Fatalf("expected multiple distinct heartbeat timestamps across %d beats, got %d unique", len(beats), len(seenAt))
	}

	if sc.Expect.GracefulShutdown {
		assertGracefulShutdown(t, p)
	}
}

// startTimingHeartbeatOnly runs the REAL Timing binary with its simulator OFF: no
// laps, no register-first lookups, no Identity dependency — just the blueprint
// skeleton (heartbeat, structured logs, graceful shutdown) Story 1.3 built and this
// story proves is cross-language equivalent.
func startTimingHeartbeatOnly(t *testing.T, amqpURL string, intervalMs int) *svcProc {
	t.Helper()
	exe := buildBinary(t, "timing")
	dbPath := filepath.Join(t.TempDir(), "timing.db")
	live := filepath.Join(t.TempDir(), "timing.live")
	env := map[string]string{
		"SIMULATOR_ENABLED":     "false",
		"HEARTBEAT_INTERVAL_MS": strconv.Itoa(intervalMs),
		"DB_PATH":               dbPath,
		"CONTRACT_DIR":          filepath.Join(repoRoot(t), "contract"),
		"LIVENESS_FILE":         live,
		"SERVICE_NAME":          "timing",
		"LOG_LEVEL":             "info",
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

// heartbeatMsg is the minimal decode of an observed control.heartbeat envelope.
type heartbeatMsg struct {
	Type   string `json:"type"`
	Source string `json:"source"`
	Data   struct {
		Service    string `json:"service"`
		At         string `json:"at"`
		InstanceID string `json:"instanceId"`
	} `json:"data"`
}

// observeHeartbeats connects to the broker as an independent observer (never the
// service under test's own connection), binds a temporary exclusive queue to
// exchange on the control.heartbeat routing key, and collects deliveries until
// minCount is reached or window elapses. This proves the heartbeat on the WIRE, not
// through any service-side introspection.
func observeHeartbeats(t *testing.T, amqpURL, exchange, expectSource string, minCount int, window time.Duration) []heartbeatMsg {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("observer dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("observer channel: %v", err)
	}
	if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("observer exchange declare: %v", err)
	}
	q, err := ch.QueueDeclare("", false, true, true, false, nil) // anonymous, autodelete, exclusive
	if err != nil {
		t.Fatalf("observer queue declare: %v", err)
	}
	if err := ch.QueueBind(q.Name, "control.heartbeat", exchange, false, nil); err != nil {
		t.Fatalf("observer queue bind: %v", err)
	}
	deliveries, err := ch.Consume(q.Name, "", true, true, false, false, nil) // auto-ack: read-only observer
	if err != nil {
		t.Fatalf("observer consume: %v", err)
	}

	var beats []heartbeatMsg
	deadline := time.After(window)
	for len(beats) < minCount {
		select {
		case d := <-deliveries:
			var m heartbeatMsg
			if err := json.Unmarshal(d.Body, &m); err != nil {
				t.Fatalf("observer decode heartbeat: %v (body=%s)", err, d.Body)
			}
			if m.Data.Service != expectSource {
				continue // a stray heartbeat from an unrelated process on a shared exchange name
			}
			beats = append(beats, m)
		case <-deadline:
			return beats
		}
	}
	return beats
}

// assertHeartbeatShape checks the observed heartbeat matches the /contract shape the
// service is required to publish (type, source, and the wire timestamp pattern this
// story's Task 1/3 codegen fixes specifically protect — exactly 3-digit millis, 'Z').
func assertHeartbeatShape(t *testing.T, b heartbeatMsg, wantService string) {
	t.Helper()
	if b.Type != "control.heartbeat" {
		t.Errorf("heartbeat type = %q, want control.heartbeat", b.Type)
	}
	if b.Source != wantService {
		t.Errorf("heartbeat source = %q, want %q", b.Source, wantService)
	}
	if b.Data.Service != wantService {
		t.Errorf("heartbeat data.service = %q, want %q", b.Data.Service, wantService)
	}
	if b.Data.InstanceID == "" {
		t.Error("heartbeat data.instanceId is empty")
	}
	if !wireTimePattern.MatchString(b.Data.At) {
		t.Errorf("heartbeat data.at = %q does not match the wire timestamp pattern (exactly 3-digit millis, literal Z)", b.Data.At)
	}
}

// assertGracefulShutdown sends SIGTERM to the running process and asserts it exits
// with code 0 within a bounded time (Story 3.1 AC4 — the skeleton's graceful-
// shutdown mechanics are cross-language equivalent, not just per-language unit
// tested). SIGTERM delivery via os.Process.Signal is POSIX; the authoritative gate is
// Linux CI (ubuntu-latest), same platform assumption every other conformance
// scenario already makes. Go's os.Process.Signal does not support arbitrary Unix
// signals on Windows at all ("not supported by windows", not a permissions/timing
// issue) — SKIP (not fail) this one assertion on that platform so local Windows dev
// isn't blocked by a Go stdlib limitation unrelated to the service under test; every
// other assertion in this scenario (heartbeat cadence/format observed on the wire)
// still runs and gates locally exactly as it does in CI.
func assertGracefulShutdown(t *testing.T, p *svcProc) {
	t.Helper()
	err := p.signalAndWait(syscall.SIGTERM, 10*time.Second)
	if err == nil {
		return
	}
	if strings.HasPrefix(err.Error(), "send signal:") && runtime.GOOS == "windows" {
		t.Skipf("SIGTERM delivery unsupported by Go on windows (%v) — graceful-shutdown proof runs on the Linux CI gate", err)
	}
	t.Fatalf("graceful shutdown after SIGTERM failed: %v\nlog:\n%s", err, p.out.String())
}
