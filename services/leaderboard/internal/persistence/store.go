package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
)

// Session lifecycle statuses (Story 1.8). The gate is FORWARD-ONLY:
// implicit -> active -> finished. 'implicit' is the NFR24 pre-start board (laps
// seen before their session.started); 'finished' is terminal — a late or
// replayed start never reopens a session.
const (
	StatusImplicit = "implicit"
	StatusActive   = "active"
	StatusFinished = "finished"
)

// Board is the current display state: the latest-first-seen session (highest
// epoch) and its ordered driver bests. Status is the STORED status — the web
// layer maps implicit to the displayed "active" (FR45's vocabulary).
type Board struct {
	SessionID string
	Status    string
	StartedAt string // wire timestamp, "" until the session.started has been seen
	EndedAt   string // wire timestamp, "" until the session.ended has been seen
	Bests     []domain.DriverBest
}

// Store is the read-model: the idempotent inbox + the session-keyed standings
// projection + the session lifecycle table.
type Store struct{ db *sql.DB }

// NewStore wraps an open DB (see Open).
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ApplyLap consumes one lap.recorded ATOMICALLY: in a single transaction it
// checks the inbox (dedupe on envelope id), and only if unseen it ensures the
// lap's session exists (first sight via a lap = the NFR24 IMPLICIT board),
// folds the lap into that session's standings (upserting the driver's best when
// the lap improves it), and records the inbox row. The whole thing commits
// together — a crash can neither double-apply nor apply-without-marking.
//
// Returns:
//
//	applied   = the read-model visibly changed (best improved/initialized, or
//	            the lap implicit-created its session)
//	duplicate = the envelope id was already in the inbox (a no-op redelivery, M6)
func (s *Store) ApplyLap(ctx context.Context, envelopeID, eventType, processedAt, sessionID string, lap domain.Lap) (applied bool, duplicate bool, err error) {
	err = WithinTx(ctx, s.db, func(tx *sql.Tx) error {
		seen, herr := hasSeen(ctx, tx, envelopeID)
		if herr != nil {
			return herr
		}
		if seen {
			duplicate = true
			return nil
		}
		created, serr := ensureSession(ctx, tx, sessionID, StatusImplicit, "", "")
		if serr != nil {
			return serr
		}
		prev, gerr := getBest(ctx, tx, sessionID, lap.MasterID)
		if gerr != nil {
			return gerr
		}
		if domain.ImprovesBest(prev, lap) {
			if uerr := upsertBest(ctx, tx, sessionID, domain.DriverBest{
				MasterID:   lap.MasterID,
				BestLapMs:  lap.LapTimeMs,
				BestLapAt:  lap.At,
				BestLapSeq: lap.Seq,
			}); uerr != nil {
				return uerr
			}
			applied = true
		}
		applied = applied || created
		return markSeen(ctx, tx, envelopeID, eventType, processedAt)
	})
	if err != nil {
		return false, false, err
	}
	return applied, duplicate, nil
}

// ApplySessionStarted consumes one session.started atomically with the inbox.
// It UPSERTS, never deletes (the auto-reset is the epoch pointer moving, not a
// wipe): an unknown session is created ACTIVE (next epoch); a known session is
// reconciled — started_at fills in, implicit promotes to active, and finished
// stays finished (forward-only; a replayed start cannot reopen or wipe). The
// session's standings rows and its epoch are never touched.
//
// applied reports whether the stored read-model changed at all (insert, status
// promotion, or started_at fill-in) — a deliberately conservative signal: a
// change on a NON-current session also notifies, costing one redundant (but
// identical) SSE frame; only true no-ops and duplicates stay silent.
func (s *Store) ApplySessionStarted(ctx context.Context, envelopeID, eventType, processedAt, sessionID, startedAt string) (applied bool, duplicate bool, err error) {
	err = WithinTx(ctx, s.db, func(tx *sql.Tx) error {
		seen, herr := hasSeen(ctx, tx, envelopeID)
		if herr != nil {
			return herr
		}
		if seen {
			duplicate = true
			return nil
		}
		created, serr := ensureSession(ctx, tx, sessionID, StatusActive, startedAt, "")
		if serr != nil {
			return serr
		}
		if created {
			applied = true
		} else {
			res, uerr := tx.ExecContext(ctx,
				`UPDATE sessions
				    SET status     = CASE WHEN status = ? THEN ? ELSE status END,
				        started_at = COALESCE(started_at, ?)
				  WHERE session_id = ?
				    AND (status = ? OR started_at IS NULL)`,
				StatusImplicit, StatusActive, startedAt, sessionID, StatusImplicit)
			if uerr != nil {
				return fmt.Errorf("reconcile session.started: %w", uerr)
			}
			n, aerr := res.RowsAffected()
			if aerr != nil {
				return aerr
			}
			applied = n > 0
		}
		return markSeen(ctx, tx, envelopeID, eventType, processedAt)
	})
	if err != nil {
		return false, false, err
	}
	return applied, duplicate, nil
}

// ApplySessionEnded consumes one session.ended atomically with the inbox. An
// unknown session is implicit-created directly as FINISHED (an end is never
// dropped — NFR24 reconcile-not-corrupt); a known session moves to finished and
// fills ended_at. Forward-only means there is nothing past finished: a
// redelivered end on an already-finished session reports applied=false.
func (s *Store) ApplySessionEnded(ctx context.Context, envelopeID, eventType, processedAt, sessionID, endedAt string) (applied bool, duplicate bool, err error) {
	err = WithinTx(ctx, s.db, func(tx *sql.Tx) error {
		seen, herr := hasSeen(ctx, tx, envelopeID)
		if herr != nil {
			return herr
		}
		if seen {
			duplicate = true
			return nil
		}
		created, serr := ensureSession(ctx, tx, sessionID, StatusFinished, "", endedAt)
		if serr != nil {
			return serr
		}
		if created {
			applied = true
		} else {
			res, uerr := tx.ExecContext(ctx,
				`UPDATE sessions
				    SET status   = ?,
				        ended_at = COALESCE(ended_at, ?)
				  WHERE session_id = ?
				    AND (status != ? OR ended_at IS NULL)`,
				StatusFinished, endedAt, sessionID, StatusFinished)
			if uerr != nil {
				return fmt.Errorf("reconcile session.ended: %w", uerr)
			}
			n, aerr := res.RowsAffected()
			if aerr != nil {
				return aerr
			}
			applied = n > 0
		}
		return markSeen(ctx, tx, envelopeID, eventType, processedAt)
	})
	if err != nil {
		return false, false, err
	}
	return applied, duplicate, nil
}

// CurrentBoard returns the latest-first-seen session (MAX(epoch)) with its
// ordered bests (best lap asc, earliest-set, ingest seq — the same order
// contract the 1.7 AllBests had, now per session). Both reads run in ONE
// transaction so the served session/standings pair is consistent (a consume
// committing between them cannot pair a stale status with newer rows). A fresh
// database with no session yet returns (nil, nil) — the waiting state.
func (s *Store) CurrentBoard(ctx context.Context) (*Board, error) {
	var b Board
	none := false
	err := WithinTx(ctx, s.db, func(tx *sql.Tx) error {
		var startedAt, endedAt sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT session_id, status, started_at, ended_at
			   FROM sessions
			  ORDER BY epoch DESC
			  LIMIT 1`).Scan(&b.SessionID, &b.Status, &startedAt, &endedAt)
		if errors.Is(err, sql.ErrNoRows) {
			none = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("query current session: %w", err)
		}
		b.StartedAt = startedAt.String
		b.EndedAt = endedAt.String

		rows, err := tx.QueryContext(ctx,
			`SELECT master_id, best_lap_ms, best_lap_at, best_lap_seq
			   FROM standings
			  WHERE session_id = ?
			  ORDER BY best_lap_ms ASC, best_lap_at ASC, best_lap_seq ASC`, b.SessionID)
		if err != nil {
			return fmt.Errorf("query standings: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var d domain.DriverBest
			if err := rows.Scan(&d.MasterID, &d.BestLapMs, &d.BestLapAt, &d.BestLapSeq); err != nil {
				return fmt.Errorf("scan standings row: %w", err)
			}
			b.Bests = append(b.Bests, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if none {
		return nil, nil
	}
	return &b, nil
}

// MaxSeq returns the highest best_lap_seq currently stored across ALL sessions
// (0 when empty). The consumer seeds its ingest counter from this on startup so
// the tie-break sequence stays monotonic across a process restart.
func (s *Store) MaxSeq(ctx context.Context) (int64, error) {
	var max sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(best_lap_seq) FROM standings`).Scan(&max); err != nil {
		return 0, fmt.Errorf("max seq: %w", err)
	}
	if !max.Valid {
		return 0, nil
	}
	return max.Int64, nil
}

// ensureSession inserts the session on FIRST SIGHT with the next epoch
// (COALESCE(MAX(epoch),0)+1 — race-free under the SQLite single-writer +
// WithinTx discipline) and the given initial status/timestamps. An existing
// session is left untouched (created=false): reconciliation of a known session
// is the caller's explicit UPDATE, so the forward-only rules stay in one place.
func ensureSession(ctx context.Context, tx *sql.Tx, sessionID, initialStatus, startedAt, endedAt string) (created bool, err error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (session_id, epoch, status, started_at, ended_at)
		 SELECT ?, COALESCE((SELECT MAX(epoch) FROM sessions), 0) + 1, ?, NULLIF(?, ''), NULLIF(?, '')
		  WHERE NOT EXISTS (SELECT 1 FROM sessions WHERE session_id = ?)`,
		sessionID, initialStatus, startedAt, endedAt, sessionID)
	if err != nil {
		return false, fmt.Errorf("ensure session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func hasSeen(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM inbox WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inbox lookup: %w", err)
	}
	return true, nil
}

func markSeen(ctx context.Context, tx *sql.Tx, id, eventType, processedAt string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO inbox (id, type, processed_at) VALUES (?, ?, ?)`,
		id, eventType, processedAt)
	if err != nil {
		return fmt.Errorf("inbox insert: %w", err)
	}
	return nil
}

func getBest(ctx context.Context, tx *sql.Tx, sessionID, masterID string) (*domain.DriverBest, error) {
	var b domain.DriverBest
	err := tx.QueryRowContext(ctx,
		`SELECT master_id, best_lap_ms, best_lap_at, best_lap_seq
		   FROM standings WHERE session_id = ? AND master_id = ?`,
		sessionID, masterID).Scan(&b.MasterID, &b.BestLapMs, &b.BestLapAt, &b.BestLapSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("standings lookup: %w", err)
	}
	return &b, nil
}

// upsertBest writes the driver's new best within the session, preserving any
// display_name overlay.
func upsertBest(ctx context.Context, tx *sql.Tx, sessionID string, b domain.DriverBest) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO standings (session_id, master_id, best_lap_ms, best_lap_at, best_lap_seq)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, master_id) DO UPDATE SET
		     best_lap_ms  = excluded.best_lap_ms,
		     best_lap_at  = excluded.best_lap_at,
		     best_lap_seq = excluded.best_lap_seq`,
		sessionID, b.MasterID, b.BestLapMs, b.BestLapAt, b.BestLapSeq)
	if err != nil {
		return fmt.Errorf("standings upsert: %w", err)
	}
	return nil
}
