package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/leaderboard/internal/domain"
)

// Store is the read-model: the idempotent inbox + the standings projection.
type Store struct{ db *sql.DB }

// NewStore wraps an open DB (see Open).
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Apply consumes one lap.recorded ATOMICALLY: in a single transaction it checks
// the inbox (dedupe on envelope id), and only if unseen it folds the lap into the
// standings projection (upserting the driver's best when the lap improves it) and
// records the inbox row. The whole thing commits together — a crash can neither
// double-apply nor apply-without-marking.
//
// Returns:
//
//	applied   = the lap improved (or initialized) the driver's best this call
//	duplicate = the envelope id was already in the inbox (a no-op redelivery, M6)
func (s *Store) Apply(ctx context.Context, envelopeID, eventType, processedAt string, lap domain.Lap) (applied bool, duplicate bool, err error) {
	err = WithinTx(ctx, s.db, func(tx *sql.Tx) error {
		seen, herr := hasSeen(ctx, tx, envelopeID)
		if herr != nil {
			return herr
		}
		if seen {
			duplicate = true
			return nil
		}
		prev, gerr := getBest(ctx, tx, lap.MasterID)
		if gerr != nil {
			return gerr
		}
		if domain.ImprovesBest(prev, lap) {
			if uerr := upsertBest(ctx, tx, domain.DriverBest{
				MasterID:   lap.MasterID,
				BestLapMs:  lap.LapTimeMs,
				BestLapAt:  lap.At,
				BestLapSeq: lap.Seq,
			}); uerr != nil {
				return uerr
			}
			applied = true
		}
		return markSeen(ctx, tx, envelopeID, eventType, processedAt)
	})
	if err != nil {
		return false, false, err
	}
	return applied, duplicate, nil
}

// AllBests returns every driver's current best, already ordered by the standings
// index (best lap asc, earliest-set, ingest seq). The pure domain.Rank applies
// the same order + positions/FL flag for rendering; reading pre-ordered keeps
// them consistent.
func (s *Store) AllBests(ctx context.Context) ([]domain.DriverBest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT master_id, best_lap_ms, best_lap_at, best_lap_seq
		   FROM standings
		  ORDER BY best_lap_ms ASC, best_lap_at ASC, best_lap_seq ASC`)
	if err != nil {
		return nil, fmt.Errorf("query standings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.DriverBest
	for rows.Next() {
		var b domain.DriverBest
		if err := rows.Scan(&b.MasterID, &b.BestLapMs, &b.BestLapAt, &b.BestLapSeq); err != nil {
			return nil, fmt.Errorf("scan standings row: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// MaxSeq returns the highest best_lap_seq currently stored (0 when empty). The
// consumer seeds its ingest counter from this on startup so the tie-break
// sequence stays monotonic across a process restart.
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

func getBest(ctx context.Context, tx *sql.Tx, masterID string) (*domain.DriverBest, error) {
	var b domain.DriverBest
	err := tx.QueryRowContext(ctx,
		`SELECT master_id, best_lap_ms, best_lap_at, best_lap_seq FROM standings WHERE master_id = ?`,
		masterID).Scan(&b.MasterID, &b.BestLapMs, &b.BestLapAt, &b.BestLapSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("standings lookup: %w", err)
	}
	return &b, nil
}

// upsertBest writes the driver's new best, preserving any display_name overlay.
func upsertBest(ctx context.Context, tx *sql.Tx, b domain.DriverBest) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO standings (master_id, best_lap_ms, best_lap_at, best_lap_seq)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(master_id) DO UPDATE SET
		     best_lap_ms  = excluded.best_lap_ms,
		     best_lap_at  = excluded.best_lap_at,
		     best_lap_seq = excluded.best_lap_seq`,
		b.MasterID, b.BestLapMs, b.BestLapAt, b.BestLapSeq)
	if err != nil {
		return fmt.Errorf("standings upsert: %w", err)
	}
	return nil
}
