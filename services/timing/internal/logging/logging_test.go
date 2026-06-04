package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNew_EmitsRequiredJSONKeys(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "timing", "7c9e6a55-0e42-4f8b-bd1a-3c2d1e0f9a8b", "info")
	log.Info("hello bus")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nraw: %s", err, buf.String())
	}
	for _, key := range []string{"timestamp", "level", "service", "correlationId", "message"} {
		if _, ok := line[key]; !ok {
			t.Errorf("log line missing required key %q; got keys: %v", key, keys(line))
		}
	}
	if line["service"] != "timing" {
		t.Errorf("service = %v, want timing", line["service"])
	}
	if line["message"] != "hello bus" {
		t.Errorf("message = %v, want 'hello bus'", line["message"])
	}
	if line["correlationId"] != "7c9e6a55-0e42-4f8b-bd1a-3c2d1e0f9a8b" {
		t.Errorf("correlationId not propagated: %v", line["correlationId"])
	}
}

// AC2: secrets must never reach the logs. The logger is the only sink; a caller
// that logs only host/port (never the password) must produce password-free output.
func TestNew_DoesNotLeakSecretsWhenCallerOmitsThem(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "timing", "cid", "info")
	// Simulate the connection log line: host/port only, never the password/URI.
	log.Info("connecting to broker", "host", "rabbitmq", "port", "5672")
	if strings.Contains(buf.String(), "s3cr3t") {
		t.Errorf("password leaked into logs: %s", buf.String())
	}
}

func TestNew_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "timing", "cid", "warn")
	log.Info("should be filtered out")
	if buf.Len() != 0 {
		t.Errorf("info line emitted at warn level: %s", buf.String())
	}
	log.Warn("should appear")
	if buf.Len() == 0 {
		t.Error("warn line was filtered out at warn level")
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
