//go:build integration

// Identity erasure integration (Story 2.6, DG-3/DG-7) against a REAL RabbitMQ broker and
// a real on-disk SQLite database (testcontainers). Reuses the rig/frontend-stand-in from
// resolve_integration_test.go (Story 2.2) — acts as the Frontend stand-in publishing
// privacy.erasure_requested to frontend.events, observes privacy.erased on
// identity.events. Proves: delete+tombstone+atomic-ack (AC1), a later lookup for the
// SAME email is held rather than re-minted (AC2, Round 33/Q33.1), and a redelivered
// erasure is idempotent.
package integration

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	goperasure "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/erasure"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/messaging"
)

func TestErasureDeletesTombstonesAndAcks(t *testing.T) {
	amqpURL := startBroker(t)
	rig := startIdentity(t, amqpURL, tempDBPath(t))
	defer rig.stop()
	obs := newObserver(t, amqpURL)
	defer obs.close()
	erObs := newErasedObserver(t, amqpURL)
	defer erObs.close()
	fe := dialFrontend(t, amqpURL)
	defer fe.close()

	// Mint via a real lookup first.
	reqID := uuid.NewString()
	fe.lookup(t, uuid.NewString(), reqID, "erase-me@example.com")
	minted := obs.await(t, reqID)

	// Erase the minted masterId.
	erasureReqID := uuid.NewString()
	fe.erasureRequest(t, uuid.NewString(), erasureReqID, minted.MasterID)
	ack := erObs.await(t, erasureReqID)

	if ack.MasterID != minted.MasterID || ack.Service != "identity" || ack.Mode != "deleted" {
		t.Fatalf("privacy.erased = %+v; want masterId=%s service=identity mode=deleted", ack, minted.MasterID)
	}
	if n := rig.identityCount(t); n != 0 {
		t.Fatalf("identities = %d; want 0 (deleted)", n)
	}
	if n := rig.tombstoneCount(t); n != 1 {
		t.Fatalf("identity_tombstones = %d; want 1", n)
	}
	if n := rig.suppressionCount(t); n != 1 {
		t.Fatalf("email_suppressions = %d; want 1", n)
	}
}

// AC2 (Round 33/Q33.1): after erasure, a NEW identity.lookup_requested for the SAME
// email must never mint again and never reply — it is held.
func TestLookupForErasedEmailIsHeldNotReminted(t *testing.T) {
	amqpURL := startBroker(t)
	rig := startIdentity(t, amqpURL, tempDBPath(t))
	defer rig.stop()
	obs := newObserver(t, amqpURL)
	defer obs.close()
	erObs := newErasedObserver(t, amqpURL)
	defer erObs.close()
	fe := dialFrontend(t, amqpURL)
	defer fe.close()

	firstReq := uuid.NewString()
	fe.lookup(t, uuid.NewString(), firstReq, "held-after-erasure@example.com")
	minted := obs.await(t, firstReq)

	erasureReqID := uuid.NewString()
	fe.erasureRequest(t, uuid.NewString(), erasureReqID, minted.MasterID)
	erObs.await(t, erasureReqID)

	// A genuinely NEW lookup (different envelope id) for the same email.
	secondReq := uuid.NewString()
	fe.lookup(t, uuid.NewString(), secondReq, "held-after-erasure@example.com")

	waitUntil(t, "held lookup persisted", func() bool { return rig.heldLookupCount(t) >= 1 })
	if c := obs.countFor(secondReq); c != 0 {
		t.Fatalf("identity.resolved replies for the held lookup = %d; want 0 (never minted, never replied)", c)
	}
	if n := rig.identityCount(t); n != 0 {
		t.Fatalf("identities = %d; want 0 (the email stays suppressed)", n)
	}
}

// A redelivered erasure (same envelope id) is idempotent: one ack, one tombstone.
func TestRedeliveredErasureIsIdempotent(t *testing.T) {
	amqpURL := startBroker(t)
	rig := startIdentity(t, amqpURL, tempDBPath(t))
	defer rig.stop()
	obs := newObserver(t, amqpURL)
	defer obs.close()
	erObs := newErasedObserver(t, amqpURL)
	defer erObs.close()
	fe := dialFrontend(t, amqpURL)
	defer fe.close()

	reqID := uuid.NewString()
	fe.lookup(t, uuid.NewString(), reqID, "redeliver-erase@example.com")
	minted := obs.await(t, reqID)

	envID := uuid.NewString()
	erasureReqID := uuid.NewString()
	fe.erasureRequest(t, envID, erasureReqID, minted.MasterID)
	erObs.await(t, erasureReqID)
	fe.erasureRequest(t, envID, erasureReqID, minted.MasterID) // redelivery — same envelope id

	// A sentinel through the single ordered queue proves the redelivery was processed.
	sentinelReq := uuid.NewString()
	fe.lookup(t, uuid.NewString(), sentinelReq, "sentinel-erase@example.com")
	obs.await(t, sentinelReq)

	if c := erObs.countFor(erasureReqID); c != 0 {
		t.Fatalf("privacy.erased for the redelivered erasure = %d extra acks; want 0 (idempotent, already consumed by the first await)", c)
	}
	if n := rig.tombstoneCount(t); n != 1 {
		t.Fatalf("identity_tombstones = %d; want 1 (no duplicate)", n)
	}
}

func tempDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "identity.db")
}

// --- frontend stand-in: privacy.erasure_requested ---------------------------

func (p *frontendPub) erasureRequest(t *testing.T, envID, requestID, masterID string) {
	t.Helper()
	env := map[string]any{
		"id": envID, "type": goperasure.ErasureRequestedType, "source": "frontend",
		"schemaVersion": 1, "envelopeVersion": 1,
		"occurredAt": "2026-08-01T11:30:00.000Z", "correlationId": requestID,
		"causationId": nil,
		"data": map[string]any{
			"requestId": requestID, "masterId": masterID,
			"requestedBy": "self", "adminActor": nil, "at": "2026-08-01T11:30:00.000Z",
		},
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal erasure request: %v", err)
	}
	if err := p.ch.PublishWithContext(context.Background(), frontendExchange,
		goperasure.ErasureRequestedType, false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		}); err != nil {
		t.Fatalf("publish erasure request: %v", err)
	}
}

// --- privacy.erased observer -------------------------------------------------

type erasedData struct {
	RequestID string `json:"requestId"`
	MasterID  string `json:"masterId"`
	Service   string `json:"service"`
	Mode      string `json:"mode"`
}

type erasedObserver struct {
	conn *amqp.Connection
	ch   *amqp.Channel
	acks chan erasedData
}

func newErasedObserver(t *testing.T, amqpURL string) *erasedObserver {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial erased observer: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("erased observer channel: %v", err)
	}
	if err := ch.ExchangeDeclare(messaging.IdentityExchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare identity.events: %v", err)
	}
	q, err := ch.QueueDeclare("privacy.erased.observer.it", false, true, true, false, nil)
	if err != nil {
		t.Fatalf("declare erased observer queue: %v", err)
	}
	if err := ch.QueueBind(q.Name, goperasure.ErasedType, messaging.IdentityExchange, false, nil); err != nil {
		t.Fatalf("bind erased observer queue: %v", err)
	}
	raw, err := ch.Consume(q.Name, "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("consume erased observer queue: %v", err)
	}
	o := &erasedObserver{conn: conn, ch: ch, acks: make(chan erasedData, 64)}
	go func() {
		for d := range raw {
			var env struct {
				Data erasedData `json:"data"`
			}
			if err := json.Unmarshal(d.Body, &env); err == nil {
				o.acks <- env.Data
			}
		}
	}()
	return o
}

func (o *erasedObserver) await(t *testing.T, requestID string) erasedData {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case a := <-o.acks:
			if a.RequestID == requestID {
				return a
			}
		case <-deadline:
			t.Fatalf("no privacy.erased for requestId %s within the deadline", requestID)
		}
	}
}

func (o *erasedObserver) countFor(requestID string) int {
	count := 0
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case a := <-o.acks:
			if a.RequestID == requestID {
				count++
			}
		case <-deadline:
			return count
		}
	}
}

func (o *erasedObserver) close() { _ = o.ch.Close(); _ = o.conn.Close() }
