package web

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func webQuietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// readSSEData reads one SSE event from r and returns the concatenated data lines.
// It blocks until a complete event (terminated by a blank line) arrives.
func readSSEData(r *bufio.Reader) (string, error) {
	var data []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(data) > 0 {
				return strings.Join(data, "\n"), nil
			}
			continue // skip leading blank lines / comments
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

// nextEvent reads one event with a failsafe deadline (guards against a hang; it
// is NOT a timing sleep — it waits on the observable arrival of an event).
func nextEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := readSSEData(r)
		ch <- res{s, err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("reading SSE event: %v", got.err)
		}
		return got.s
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an SSE event")
		return ""
	}
}

// AC4: a connecting client receives the current standings immediately, and a new
// snapshot is PUSHED on Publish (no polling, no reload) — over SSE.
func TestSSE_InitialSnapshotThenPushOnPublish(t *testing.T) {
	var mu sync.Mutex
	current := Snapshot{Rows: []RowView{{Position: 1, MasterID: "1a9f7c20-x", DisplayName: "1a9f7c20", LapTimeMs: 41000, LapTime: "0:41.000", IsFastest: true}}}
	snapFn := func() Snapshot {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	srv := NewServer(":0", snapFn, webQuietLogger())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	r := bufio.NewReader(resp.Body)

	first := nextEvent(t, r)
	if !strings.Contains(first, "1a9f7c20") || !strings.Contains(first, "41000") {
		t.Errorf("initial snapshot missing expected data: %q", first)
	}

	// Change the read-model and push.
	mu.Lock()
	current = Snapshot{Rows: []RowView{{Position: 1, MasterID: "9z", DisplayName: "9z", LapTimeMs: 39000, LapTime: "0:39.000", IsFastest: true}}}
	mu.Unlock()
	srv.Publish()

	second := nextEvent(t, r)
	if !strings.Contains(second, "39000") {
		t.Errorf("pushed snapshot after Publish missing new data: %q", second)
	}
}

// AC2: a SESSION change (not just a lap) is pushed to connected clients — the
// frame carries the new session block so the board can flip its status pill /
// auto-reset without a reload.
func TestSSE_SessionChangePushed(t *testing.T) {
	var mu sync.Mutex
	current := Snapshot{
		Session: &SessionView{SessionID: "s1", Status: "active"},
		Rows:    []RowView{{Position: 1, MasterID: "1a9f7c20-x", DisplayName: "1a9f7c20", LapTimeMs: 41000, LapTime: "0:41.000", IsFastest: true}},
	}
	snapFn := func() Snapshot {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	srv := NewServer(":0", snapFn, webQuietLogger())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	first := nextEvent(t, r)
	if !strings.Contains(first, `"sessionId":"s1"`) || !strings.Contains(first, `"status":"active"`) {
		t.Errorf("initial snapshot missing the session block: %q", first)
	}

	// The session ends: same rows, new status — must still be pushed.
	mu.Lock()
	current = Snapshot{Session: &SessionView{SessionID: "s1", Status: "finished"}, Rows: current.Rows}
	mu.Unlock()
	srv.Publish()

	second := nextEvent(t, r)
	if !strings.Contains(second, `"status":"finished"`) {
		t.Errorf("pushed frame after session end missing finished status: %q", second)
	}
}

// Review fix: a slow client whose buffer is full must lose the OLDEST frame,
// never the newest — a terminal session-finished snapshot may be the last push
// ever and has to survive the overflow.
func TestHub_OverflowKeepsNewestFrame(t *testing.T) {
	h := newHub()
	ch := h.add()
	defer h.remove(ch)

	var last []byte
	for i := 0; i < cap(ch)+4; i++ {
		last = []byte{byte('a' + i)}
		h.broadcast(last)
	}

	var newestSeen bool
	for {
		select {
		case b := <-ch:
			if string(b) == string(last) {
				newestSeen = true
			}
		default:
			if !newestSeen {
				t.Fatal("the newest frame was dropped on overflow; oldest must be evicted instead")
			}
			return
		}
	}
}

// The static route fails gracefully (clear status) when the SPA bundle is not
// built (only the .gitkeep placeholder is embedded in the Go-only CI build).
func TestStaticRoot_DegradesGracefullyWithoutBundle(t *testing.T) {
	srv := NewServer(":0", func() Snapshot { return Snapshot{Rows: []RowView{}} }, webQuietLogger())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Either the built bundle is present (200) or a clear 503 — never a panic/500.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET / status = %d, want 200 (bundle built) or 503 (not built)", resp.StatusCode)
	}
}
