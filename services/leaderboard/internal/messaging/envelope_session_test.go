package messaging

import (
	"os"
	"path/filepath"
	"testing"
)

// readExample loads a committed /contract example fixture (the real wire shapes
// the decoders must read — tolerant reader, only the fields the board needs).
func readExample(t *testing.T, rel string) []byte {
	t.Helper()
	dir, err := ResolveContractDir("")
	if err != nil {
		t.Fatalf("resolve /contract: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	return b
}

// AC2: the session.started decoder reads sessionId + startedAt from the
// committed example (the only fields the Leaderboard needs).
func TestDecodeSessionStarted_FromCommittedExample(t *testing.T) {
	env, err := DecodeIncoming(readExample(t, "examples/timing/session.started.v1.example.json"))
	if err != nil {
		t.Fatalf("DecodeIncoming: %v", err)
	}
	if env.Type != SessionStartedRoutingKey {
		t.Fatalf("type = %q, want %q", env.Type, SessionStartedRoutingKey)
	}
	data, err := DecodeSessionStarted(env)
	if err != nil {
		t.Fatalf("DecodeSessionStarted: %v", err)
	}
	if data.SessionID != "session-2026-05-31-evening-heat-3" {
		t.Errorf("SessionID = %q, want the example's id", data.SessionID)
	}
	if data.StartedAt != "2026-05-31T13:45:00.000Z" {
		t.Errorf("StartedAt = %q, want the example's timestamp", data.StartedAt)
	}
}

// AC2: the session.ended decoder reads sessionId + endedAt — and deliberately
// NOTHING else: summary[] item shape is intentionally unpinned in v1 (the
// board's finished standings are its own projection).
func TestDecodeSessionEnded_FromCommittedExample(t *testing.T) {
	env, err := DecodeIncoming(readExample(t, "examples/timing/session.ended.v1.example.json"))
	if err != nil {
		t.Fatalf("DecodeIncoming: %v", err)
	}
	if env.Type != SessionEndedRoutingKey {
		t.Fatalf("type = %q, want %q", env.Type, SessionEndedRoutingKey)
	}
	data, err := DecodeSessionEnded(env)
	if err != nil {
		t.Fatalf("DecodeSessionEnded: %v", err)
	}
	if data.SessionID != "session-2026-05-31-evening-heat-3" {
		t.Errorf("SessionID = %q, want the example's id", data.SessionID)
	}
	if data.EndedAt != "2026-05-31T14:05:00.000Z" {
		t.Errorf("EndedAt = %q, want the example's timestamp", data.EndedAt)
	}
}
