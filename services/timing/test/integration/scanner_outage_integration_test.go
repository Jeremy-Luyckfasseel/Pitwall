//go:build integration

// Scanner-offline integration (Story 3.5, FR38) against a REAL RabbitMQ broker and a real
// on-disk SQLite database. Proves the persist-first/never-faked path end-to-end: with an
// injected outage the simulator drops a run of crossings (fewer lap.recorded on the bus than
// a full session — the honest gap), emits exactly one scanner.offline and one scanner.online
// through the Story-1.4 outbox, and Timing durably records the outage in scanner_outages and
// closes it on recovery. No missed crossing is ever faked into a lap.
package integration

import (
	"context"
	"database/sql"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/logging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/persistence"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/relay"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/timing/internal/simulator"
)

func TestSimulatorScannerOutagePersistedAndGapNeverFaked(t *testing.T) {
	const drivers, laps, outageLaps = 3, 5, 2
	amqpURL := startBroker(t)
	store, db, closeDB := openStore(t, filepath.Join(t.TempDir(), "timing.db"))
	defer closeDB()
	outageStore := persistence.NewScannerOutageStore(db)

	deliveries, closeConsumer := bindConsumerAll(t, amqpURL)
	defer closeConsumer()

	pub, confirmCh := dialConfirm(t, amqpURL)
	defer pub.Close()
	defer confirmCh.Close()

	r := relay.New(relay.Config{
		Store:    store,
		Validate: validatorFor(t),
		Publish:  confirmCh.PublishConfirmed,
		Interval: 50 * time.Millisecond,
		Log:      logging.New(testWriter{t}, "timing", itCorrelationID, "error"),
	})
	enqueue := relay.NewEnqueuer(db, store, validatorFor(t), r)

	simLog := logging.New(testWriter{t}, "timing", itCorrelationID, "error")
	sim := simulator.New(simulator.Config{
		Drivers:           drivers,
		LapMeanMs:         45000,
		LapStddevMs:       2000,
		SessionLaps:       laps,
		MinLapTimeMs:      10000,
		Tick:              0,
		SessionGap:        time.Hour,
		Source:            "timing",
		Rng:               rand.New(rand.NewSource(21)),
		Now:               time.Now,
		Enqueue:           enqueue,
		Resolve:           fakeResolveID,
		ScannerOutageLaps: outageLaps,
		OpenOutage: func(ctx context.Context, scannerID, sessionID, gapFrom, since, recordedAt string) (int64, error) {
			return outageStore.OpenOutage(ctx, scannerID, sessionID, gapFrom, since, recordedAt)
		},
		CloseOutage: outageStore.CloseOutage,
		Log:         simLog,
	})

	relayCtx, stopRelay := context.WithCancel(context.Background())
	defer stopRelay()
	simCtx, stopSim := context.WithCancel(context.Background())
	defer stopSim()
	go func() { _ = r.Run(relayCtx) }()
	go func() { _ = sim.Run(simCtx) }()

	validate := validatorFor(t)
	gotLaps, offline, online := 0, 0, 0
	deadline := time.After(30 * time.Second)
	for {
		select {
		case d := <-deliveries:
			env, err := messaging.DecodeIncoming(d.Body)
			if err != nil {
				t.Fatalf("delivery not a valid envelope: %v", err)
			}
			if err := validate(d.Body); err != nil {
				t.Fatalf("event %q off the bus failed /contract validation: %v", env.Type, err)
			}
			switch env.Type {
			case messaging.LapRecordedRoutingKey:
				gotLaps++
			case messaging.ScannerOfflineRoutingKey:
				offline++
			case messaging.ScannerOnlineRoutingKey:
				online++
			case messaging.SessionEndedRoutingKey:
				stopSim()
				if offline != 1 || online != 1 {
					t.Fatalf("scanner events off the bus = %d offline / %d online, want 1 / 1", offline, online)
				}
				if gotLaps >= drivers*laps {
					t.Fatalf("lap.recorded count = %d, want fewer than a full session %d (the honest gap)", gotLaps, drivers*laps)
				}
				if gotLaps == 0 {
					t.Fatalf("lap.recorded count = 0, want laps before and after the gap")
				}
				assertAllSentEventually(t, store)
				stopRelay()
				assertOutageClosed(t, db)
				return
			}
		case <-deadline:
			t.Fatalf("did not observe a full session in time; saw %d laps, %d offline, %d online", gotLaps, offline, online)
		}
	}
}

// assertOutageClosed polls scanner_outages until a row lands, then checks it is well-formed
// and CLOSED (online_at set on recovery).
func assertOutageClosed(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		var scannerID, gapFrom, since string
		var onlineAt sql.NullString
		err := db.QueryRow(`SELECT scanner_id, gap_from, since, online_at FROM scanner_outages LIMIT 1`).
			Scan(&scannerID, &gapFrom, &since, &onlineAt)
		if err == nil {
			if scannerID != messaging.ScannerID {
				t.Errorf("scanner_outages scanner_id = %q, want %q", scannerID, messaging.ScannerID)
			}
			if gapFrom == "" || since == "" {
				t.Errorf("scanner_outages gap_from/since empty: %q/%q", gapFrom, since)
			}
			if !onlineAt.Valid {
				t.Errorf("scanner_outages online_at is NULL — the outage was never closed on recovery")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("no row landed in scanner_outages in time: %v", err)
		case <-tick.C:
		}
	}
}
