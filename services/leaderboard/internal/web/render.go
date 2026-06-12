// Package web serves the live trackside board: the //go:embed-ed Vite/React SPA
// and an SSE endpoint that pushes the board snapshot on connect and on every
// read-model change (Q26.1: SSE, never HTTP polling, never an HTTP /health). The
// render mapper turns the current session's board into the JSON shape the SPA
// renders — including the session status block (FR45).
package web

import (
	"fmt"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/persistence"
)

// RowView is one rendered standings row (the wire shape the SSE stream sends and
// the leaderboard-row component renders — UX-DR8).
type RowView struct {
	Position    int    `json:"position"`
	MasterID    string `json:"masterId"`
	DisplayName string `json:"displayName"` // nickname overlay if present, else short masterId
	LapTimeMs   int64  `json:"lapTimeMs"`
	LapTime     string `json:"lapTime"` // formatted m:ss.mmm for mono/tabular display
	IsFastest   bool   `json:"isFastest"`
}

// SessionView is the session status block (FR45). Status uses the DISPLAY
// vocabulary: "active" | "finished" (the stored 'implicit' gate renders as
// active — laps are physically flowing; implicit-vs-active is an NFR24
// reconciliation detail, not a spectator-facing state).
type SessionView struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
}

// Snapshot is the full board state pushed over SSE. Session is null only before
// any session has ever been seen (the SPA's waiting state).
type Snapshot struct {
	Session *SessionView `json:"session"`
	Rows    []RowView    `json:"rows"`
}

// ToSnapshot maps the current session's board (nil when no session has ever
// been seen) to the render shape: ranked rows (best lap asc, first-to-set
// tie-break) plus the session status block. Display name falls back to the
// short masterId — Epic 1 has no nicknames (Q26.3); the overlay arrives in Epic 3.
func ToSnapshot(board *persistence.Board) Snapshot {
	if board == nil {
		return Snapshot{Rows: []RowView{}}
	}
	ranked := domain.Rank(board.Bests)
	rows := make([]RowView, 0, len(ranked))
	for _, r := range ranked {
		rows = append(rows, RowView{
			Position:    r.Position,
			MasterID:    r.MasterID,
			DisplayName: domain.ShortMasterID(r.MasterID),
			LapTimeMs:   r.BestLapMs,
			LapTime:     formatLapTime(r.BestLapMs),
			IsFastest:   r.IsFastest,
		})
	}
	return Snapshot{
		Session: &SessionView{SessionID: board.SessionID, Status: displayStatus(board.Status)},
		Rows:    rows,
	}
}

// displayStatus maps the stored lifecycle gate to FR45's two display states.
func displayStatus(stored string) string {
	if stored == persistence.StatusFinished {
		return "finished"
	}
	return "active"
}

// formatLapTime renders milliseconds as m:ss.mmm (minutes have no leading zero;
// seconds and millis are zero-padded) — the mono/tabular lap-time format.
func formatLapTime(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	minutes := ms / 60000
	seconds := (ms % 60000) / 1000
	millis := ms % 1000
	return fmt.Sprintf("%d:%02d.%03d", minutes, seconds, millis)
}
