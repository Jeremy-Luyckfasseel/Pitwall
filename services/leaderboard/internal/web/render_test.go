package web

import (
	"encoding/json"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/persistence"
)

// AC1 regression: the snapshot rows are ordered, positioned, fastest-flagged,
// with the short-masterId display fallback and a formatted (mono/tabular) time.
func TestToSnapshot_OrdersPositionsAndFlags(t *testing.T) {
	snap := ToSnapshot(&persistence.Board{
		SessionID: "s1",
		Status:    persistence.StatusActive,
		Bests: []domain.DriverBest{
			{MasterID: "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", BestLapMs: 41000, BestLapAt: "2026-06-08T10:00:01.000Z", BestLapSeq: 1},
			{MasterID: "2b8e6d31-4f95-4e22-8bb3-6c7d5e4f3a10", BestLapMs: 42000, BestLapAt: "2026-06-08T10:00:02.000Z", BestLapSeq: 2},
		},
	})
	if len(snap.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(snap.Rows))
	}
	r0 := snap.Rows[0]
	if r0.Position != 1 || !r0.IsFastest {
		t.Errorf("rank-1 row = %+v, want Position 1 + IsFastest", r0)
	}
	if r0.DisplayName != "1a9f7c20" {
		t.Errorf("DisplayName = %q, want short masterId 1a9f7c20", r0.DisplayName)
	}
	if r0.LapTimeMs != 41000 {
		t.Errorf("LapTimeMs = %d, want 41000", r0.LapTimeMs)
	}
	if r0.LapTime == "" {
		t.Error("LapTime (formatted) must be non-empty for mono/tabular rendering")
	}
	if snap.Rows[1].IsFastest {
		t.Error("only rank 1 may be flagged fastest")
	}
}

// AC2: the session block carries the sessionId + display status. The stored
// statuses map to FR45's display vocabulary: active -> active,
// implicit -> active (laps are physically flowing), finished -> finished.
func TestToSnapshot_SessionStatusMapping(t *testing.T) {
	cases := []struct {
		stored string
		want   string
	}{
		{persistence.StatusActive, "active"},
		{persistence.StatusImplicit, "active"},
		{persistence.StatusFinished, "finished"},
	}
	for _, c := range cases {
		snap := ToSnapshot(&persistence.Board{SessionID: "s1", Status: c.stored})
		if snap.Session == nil {
			t.Fatalf("stored %q: Session block missing", c.stored)
		}
		if snap.Session.SessionID != "s1" || snap.Session.Status != c.want {
			t.Errorf("stored %q: session = %+v, want s1/%s", c.stored, snap.Session, c.want)
		}
	}
}

// Before any session has ever been seen the session block is null (the SPA's
// waiting state) and rows serialize as [] not null.
func TestToSnapshot_NilBoard_WaitingState(t *testing.T) {
	snap := ToSnapshot(nil)
	if snap.Session != nil {
		t.Errorf("nil board: Session = %+v, want nil", snap.Session)
	}
	if snap.Rows == nil || len(snap.Rows) != 0 {
		t.Errorf("nil board: Rows = %#v, want empty non-nil slice", snap.Rows)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if s != `{"session":null,"rows":[],"stale":false,"connection":""}` {
		t.Errorf("wire shape = %s, want {\"session\":null,\"rows\":[],\"stale\":false,\"connection\":\"\"}", s)
	}
}

// AC1 (Story 1.10): the served bundle carries an honest connection state. When
// connected it is live (not stale); when the bus is down it is flagged
// stale/reconnecting — and the last-known board (session + rows) is preserved, so
// the board freezes on last-known rather than blanking or faking live.
func TestSnapshot_WithConnection(t *testing.T) {
	base := ToSnapshot(&persistence.Board{
		SessionID: "s1",
		Status:    persistence.StatusActive,
		Bests:     []domain.DriverBest{{MasterID: "m1", BestLapMs: 41000, BestLapAt: "t", BestLapSeq: 1}},
	})

	live := base.WithConnection(true)
	if live.Stale {
		t.Errorf("connected: Stale = true, want false")
	}
	if live.Connection != "live" {
		t.Errorf("connected: Connection = %q, want \"live\"", live.Connection)
	}

	down := base.WithConnection(false)
	if !down.Stale {
		t.Errorf("disconnected: Stale = false, want true")
	}
	if down.Connection != "reconnecting" {
		t.Errorf("disconnected: Connection = %q, want \"reconnecting\"", down.Connection)
	}
	// The stale board still serves its last-known session + rows (frozen, flagged).
	if down.Session == nil || down.Session.SessionID != "s1" || len(down.Rows) != 1 {
		t.Errorf("stale board must preserve last-known session/rows, got %+v rows=%d", down.Session, len(down.Rows))
	}
}

// A fresh session with no laps yet: session block present, rows empty (the
// auto-reset as a connected client sees it).
func TestToSnapshot_FreshSession_EmptyRows(t *testing.T) {
	snap := ToSnapshot(&persistence.Board{SessionID: "s2", Status: persistence.StatusActive})
	if snap.Session == nil || snap.Session.SessionID != "s2" || snap.Session.Status != "active" {
		t.Errorf("session = %+v, want s2 active", snap.Session)
	}
	if snap.Rows == nil || len(snap.Rows) != 0 {
		t.Errorf("rows = %#v, want empty non-nil slice", snap.Rows)
	}
}

func TestFormatLapTime(t *testing.T) {
	cases := map[int64]string{
		42318:  "0:42.318",
		102318: "1:42.318",
		1001:   "0:01.001",
		60000:  "1:00.000",
	}
	for ms, want := range cases {
		if got := formatLapTime(ms); got != want {
			t.Errorf("formatLapTime(%d) = %q, want %q", ms, got, want)
		}
	}
}
