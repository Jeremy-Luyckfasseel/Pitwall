package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/domain"
)

// DriverPRStore is Timing's LOCAL copy of each driver's all-time personal record
// (Story 3.4, FR37) — an ECST cache, NOT the system of record (Driver owns the
// canonical PR). It is written two ways: ObserveLap (live detection, advances the
// copy optimistically on each break, Q37.3) and Refresh (overwrite with Driver's
// confirmed canonical value on a consumed driver.pr_updated, latest-confirmed-wins).
type DriverPRStore struct {
	db *sql.DB
}

// NewDriverPRStore binds the store to an already-open, already-migrated database.
func NewDriverPRStore(db *sql.DB) *DriverPRStore {
	return &DriverPRStore{db: db}
}

// Get returns the driver's locally-held all-time PR (ms), or ok=false if none is held
// yet. A false ok is NOT an error — it simply means the next counted lap is a first PR.
func (s *DriverPRStore) Get(ctx context.Context, masterID string) (bestLapMs int64, ok bool, err error) {
	return getPRWith(ctx, s.db, masterID)
}

func getPRWith(ctx context.Context, q execer, masterID string) (bestLapMs int64, ok bool, err error) {
	err = q.QueryRowContext(ctx,
		`SELECT best_lap_ms FROM driver_prs WHERE master_id = ?`, masterID).Scan(&bestLapMs)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("get pr %q: %w", masterID, err)
	}
	return bestLapMs, true, nil
}

// ObserveLap applies the pure PR rule (domain.CheckPR) to one counted lap and, ONLY
// on a break, advances the local copy so the next lap compares against the new best
// (Q37.3, optimistic advance). The read-detect-advance runs inside one transaction so
// two crossings for the same driver can never interleave and lose a break. It returns
// the detector's verdict (broken + the beaten value, nil on a first PR) so the caller
// can build the personal_record.broken event.
func (s *DriverPRStore) ObserveLap(ctx context.Context, masterID, sessionID string, lapTimeMs int64, at string) (broken bool, previousMs *int64, err error) {
	err = WithinTx(ctx, s.db, func(tx *sql.Tx) error {
		current, ok, rerr := getPRWith(ctx, tx, masterID)
		if rerr != nil {
			return fmt.Errorf("observe lap %q: %w", masterID, rerr)
		}
		var cur *int64
		if ok {
			cur = &current
		}
		b, prev := domain.CheckPR(cur, lapTimeMs)
		if b {
			if uerr := upsertPRWith(ctx, tx, masterID, lapTimeMs, sessionID, at, at); uerr != nil {
				return fmt.Errorf("observe lap %q: advance: %w", masterID, uerr)
			}
		}
		broken, previousMs = b, prev
		return nil
	})
	if err != nil {
		return false, nil, err
	}
	return broken, previousMs, nil
}

// Refresh overwrites the local copy with Driver's confirmed canonical value on a
// consumed driver.pr_updated (AC2, latest-confirmed-wins), seeding the row if the
// driver has no local copy yet (never dropped). setAt is the record-setting lap's wire
// time (carried by driver.pr_updated); now is when Timing processed the refresh.
func (s *DriverPRStore) Refresh(ctx context.Context, masterID string, lapTimeMs int64, setAt, now string) error {
	// sessionId is unknown from driver.pr_updated (it carries masterId/lapTimeMs/setAt),
	// so the confirmation leaves session_id NULL — the cache only needs the best time to
	// detect the next break.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO driver_prs (master_id, best_lap_ms, session_id, set_at, updated_at)
		 VALUES (?, ?, NULL, ?, ?)
		 ON CONFLICT(master_id) DO UPDATE SET
		     best_lap_ms = excluded.best_lap_ms,
		     set_at      = excluded.set_at,
		     updated_at  = excluded.updated_at`,
		masterID, lapTimeMs, setAt, now)
	if err != nil {
		return fmt.Errorf("refresh pr %q: %w", masterID, err)
	}
	return nil
}

func upsertPRWith(ctx context.Context, q execer, masterID string, bestLapMs int64, sessionID, setAt, now string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO driver_prs (master_id, best_lap_ms, session_id, set_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(master_id) DO UPDATE SET
		     best_lap_ms = excluded.best_lap_ms,
		     session_id  = excluded.session_id,
		     set_at      = excluded.set_at,
		     updated_at  = excluded.updated_at`,
		masterID, bestLapMs, sessionID, setAt, now)
	if err != nil {
		return fmt.Errorf("upsert pr %q: %w", masterID, err)
	}
	return nil
}
