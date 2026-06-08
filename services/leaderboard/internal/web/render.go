// Package web serves the live trackside board: the //go:embed-ed Vite/React SPA
// and an SSE endpoint that pushes the standings snapshot on connect and on every
// read-model change (Q26.1: SSE, never HTTP polling, never an HTTP /health). The
// render mapper turns the domain standings into the JSON shape the SPA renders.
package web

import (
	"fmt"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
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

// Snapshot is the full board state pushed over SSE.
type Snapshot struct {
	Rows []RowView `json:"rows"`
}

// ToSnapshot ranks the driver bests (best lap asc, first-to-set tie-break) and
// maps each to a render row. Display name falls back to the short masterId — Epic
// 1 has no nicknames or racing numbers (Q26.3); the overlay arrives in Epic 3.
func ToSnapshot(bests []domain.DriverBest) Snapshot {
	ranked := domain.Rank(bests)
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
	return Snapshot{Rows: rows}
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
