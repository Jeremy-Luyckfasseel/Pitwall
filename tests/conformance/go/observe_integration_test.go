//go:build integration

package conformance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// snapshotView mirrors the Leaderboard's served SSE bundle (render.go Snapshot).
// The harness depends only on the WIRE shape, not the service's Go types — it is
// a separate module observing a real binary over HTTP.
type snapshotView struct {
	Session    *sessionView `json:"session"`
	Rows       []rowView    `json:"rows"`
	Stale      bool         `json:"stale"`
	Connection string       `json:"connection"`
}

type sessionView struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
}

type rowView struct {
	Position  int    `json:"position"`
	MasterID  string `json:"masterId"`
	LapTimeMs int64  `json:"lapTimeMs"`
}

// fetchSnapshot connects to the Leaderboard SSE endpoint and reads the FIRST
// data: frame — the board snapshot the server pushes immediately on connect
// (server.go handleEvents). This is the harness's primary observable.
func fetchSnapshot(baseURL string) (snapshotView, error) {
	var snap snapshotView
	req, err := http.NewRequest(http.MethodGet, baseURL+"/events", nil)
	if err != nil {
		return snap, err
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return snap, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return snap, fmt.Errorf("GET /events: status %d", resp.StatusCode)
	}
	data, err := readFirstSSEData(resp.Body)
	if err != nil {
		return snap, err
	}
	if err := json.Unmarshal([]byte(data), &snap); err != nil {
		return snap, fmt.Errorf("decode snapshot %q: %w", data, err)
	}
	return snap, nil
}

// readFirstSSEData reads SSE lines until the first complete "data:" payload,
// terminated by the blank line that ends a frame (writeSSE in server.go writes
// "data: <json>\n\n").
func readFirstSSEData(r interface{ Read([]byte) (int, error) }) (string, error) {
	sc := bufio.NewScanner(bufio.NewReader(readerFunc(r.Read)))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var payload strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			payload.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			if payload.Len() > 0 {
				return payload.String(), nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if payload.Len() > 0 {
		return payload.String(), nil
	}
	return "", fmt.Errorf("no SSE data frame received")
}

// readerFunc adapts a Read method value to an io.Reader.
type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// waitUntil polls cond until it is true or the timeout elapses. The sleep here is
// a POLL INTERVAL, never an assertion of timing (AR16 "wait on observable
// conditions, never sleep").
func waitUntil(t *testing.T, desc string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for: %s", timeout, desc)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// assertRankedAscending verifies the served board is ordered by best lap
// ascending (the read-model's ranking — domain.Rank) AND that positions are the
// contiguous 1..N — so a ranking bug producing ties, gaps, or duplicate positions
// is caught, not just a non-monotonic lap time.
func assertRankedAscending(t *testing.T, snap snapshotView) {
	t.Helper()
	for i := 1; i < len(snap.Rows); i++ {
		if snap.Rows[i-1].LapTimeMs > snap.Rows[i].LapTimeMs {
			t.Fatalf("board not ranked ascending at row %d: %d > %d",
				i, snap.Rows[i-1].LapTimeMs, snap.Rows[i].LapTimeMs)
		}
	}
	for i, r := range snap.Rows {
		if r.Position != i+1 {
			t.Fatalf("positions not contiguous 1..N: row %d has position %d", i, r.Position)
		}
	}
}

// masterIDs returns the set of driver masterIds currently on the board.
func masterIDs(snap snapshotView) map[string]int64 {
	out := make(map[string]int64, len(snap.Rows))
	for _, r := range snap.Rows {
		out[r.MasterID] = r.LapTimeMs
	}
	return out
}
