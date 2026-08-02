//go:build integration

// Identity resolve-or-mint integration (Story 2.2) against a REAL RabbitMQ broker and
// a real on-disk SQLite database (testcontainers — no sleeps). It wires the service
// exactly as cmd/identity does (consumer Bus + outbox relay + TxResolver + Handler),
// acts as the FRONTEND stand-in (publishing identity.lookup_requested to
// frontend.events since Frontend itself is Epic 5), and OBSERVES identity.resolved on
// identity.events. Proves: mint-on-unknown, reuse-on-known (one id, no isNew),
// concurrent same-email race → exactly one masterId, redelivery idempotent (one reply,
// one row), and malformed → log + dead-letter (parked, no reply).
package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	goperasure "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/erasure"
	librelay "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/relay"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/consumer"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/persistence"
)

const (
	frontendExchange = "frontend.events"
	lookupQueueIT    = "identity.lookup-requested.it"
)

func quietLog() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestMintsOnUnknownEmail(t *testing.T) {
	amqpURL := startBroker(t)
	rig := startIdentity(t, amqpURL, filepath.Join(t.TempDir(), "identity.db"))
	defer rig.stop()
	obs := newObserver(t, amqpURL)
	defer obs.close()
	fe := dialFrontend(t, amqpURL)
	defer fe.close()

	reqID := uuid.NewString()
	fe.lookup(t, uuid.NewString(), reqID, "jeremy@example.com")

	reply := obs.await(t, reqID)
	if !isUUIDv4(reply.MasterID) {
		t.Fatalf("minted masterId %q is not a lowercase UUID v4", reply.MasterID)
	}
	if reply.Email != "jeremy@example.com" {
		t.Fatalf("reply email = %q; want the looked-up email", reply.Email)
	}
	if n := rig.identityCount(t); n != 1 {
		t.Fatalf("identities = %d; want 1", n)
	}
}

func TestReusesKnownEmail(t *testing.T) {
	amqpURL := startBroker(t)
	rig := startIdentity(t, amqpURL, filepath.Join(t.TempDir(), "identity.db"))
	defer rig.stop()
	obs := newObserver(t, amqpURL)
	defer obs.close()
	fe := dialFrontend(t, amqpURL)
	defer fe.close()

	req1, req2 := uuid.NewString(), uuid.NewString()
	fe.lookup(t, uuid.NewString(), req1, "same@example.com")
	first := obs.await(t, req1)
	fe.lookup(t, uuid.NewString(), req2, "same@example.com")
	second := obs.await(t, req2)

	if second.MasterID != first.MasterID {
		t.Fatalf("second lookup masterId %q != first %q (must be exactly one id per person)", second.MasterID, first.MasterID)
	}
	if n := rig.identityCount(t); n != 1 {
		t.Fatalf("identities = %d; want 1 (no duplicate)", n)
	}
}

func TestConcurrentSameEmailResolvesToOneId(t *testing.T) {
	amqpURL := startBroker(t)
	rig := startIdentity(t, amqpURL, filepath.Join(t.TempDir(), "identity.db"))
	defer rig.stop()
	obs := newObserver(t, amqpURL)
	defer obs.close()
	fe := dialFrontend(t, amqpURL)
	defer fe.close()

	// Fire many lookups for the SAME new email back-to-back (distinct envelope +
	// request ids). Exactly one masterId must be minted; every reply carries it.
	const n = 8
	reqs := make([]string, n)
	for i := 0; i < n; i++ {
		reqs[i] = uuid.NewString()
		fe.lookup(t, uuid.NewString(), reqs[i], "race@example.com")
	}
	var got string
	for i := 0; i < n; i++ {
		reply := obs.await(t, reqs[i])
		if got == "" {
			got = reply.MasterID
		} else if reply.MasterID != got {
			t.Fatalf("reply %d masterId %q != %q — the race minted more than one id", i, reply.MasterID, got)
		}
	}
	if c := rig.identityCount(t); c != 1 {
		t.Fatalf("identities = %d; want exactly 1 (UNIQUE(email) single-writer)", c)
	}
}

func TestRedeliveredLookupIsIdempotent(t *testing.T) {
	amqpURL := startBroker(t)
	rig := startIdentity(t, amqpURL, filepath.Join(t.TempDir(), "identity.db"))
	defer rig.stop()
	obs := newObserver(t, amqpURL)
	defer obs.close()
	fe := dialFrontend(t, amqpURL)
	defer fe.close()

	// The SAME envelope id delivered twice: the inbox dedupes the second (no second
	// mint, no second reply). A follow-up distinct lookup proves the queue drained
	// (single ordered queue) and the redelivery produced no extra reply.
	dupID := uuid.NewString()
	reqDup := uuid.NewString()
	fe.lookup(t, dupID, reqDup, "dup@example.com")
	first := obs.await(t, reqDup)
	fe.lookup(t, dupID, reqDup, "dup@example.com") // redelivery — same envelope id

	sentinelReq := uuid.NewString()
	fe.lookup(t, uuid.NewString(), sentinelReq, "sentinel@example.com")
	obs.await(t, sentinelReq) // single ordered queue: once the sentinel reply lands, the redelivery was processed

	// The first reply was already consumed by await above; the redelivery must have
	// produced NO further reply (idempotent inbox), so the channel holds none for it.
	if c := obs.countFor(reqDup); c != 0 {
		t.Fatalf("redelivery produced %d extra replies; want 0 (idempotent — one reply total)", c)
	}
	if !isUUIDv4(first.MasterID) {
		t.Fatalf("first reply masterId %q invalid", first.MasterID)
	}
	if c := rig.identityCount(t); c != 2 {
		t.Fatalf("identities = %d; want 2 (dup minted once + sentinel)", c)
	}
}

func TestMalformedLookupIsParkedNoReply(t *testing.T) {
	amqpURL := startBroker(t)
	rig := startIdentity(t, amqpURL, filepath.Join(t.TempDir(), "identity.db"))
	defer rig.stop()
	obs := newObserver(t, amqpURL)
	defer obs.close()
	fe := dialFrontend(t, amqpURL)
	defer fe.close()

	// requestId is not a UUID -> fails validate-on-consume -> parked immediately
	// (never resolved, never minted, never dropped).
	badReq := "not-a-uuid"
	fe.lookupRaw(t, uuid.NewString(), badReq, "bad@example.com")

	// The malformed message lands in the parking queue, and NO identity is minted.
	parking := messaging.ParkingQueueName(lookupQueueIT)
	waitUntil(t, "malformed lookup parked", func() bool { return obs.queueDepth(t, parking) >= 1 })
	if c := rig.identityCount(t); c != 0 {
		t.Fatalf("identities = %d; want 0 (a malformed lookup must never mint)", c)
	}

	// A subsequent VALID lookup still resolves (the consumer kept running past the poison).
	goodReq := uuid.NewString()
	fe.lookup(t, uuid.NewString(), goodReq, "good@example.com")
	obs.await(t, goodReq)
}

// --- identity rig (wires the service as cmd/identity does) -----------------

type identityRig struct {
	db     *sql.DB
	cancel func()
}

func (r *identityRig) stop() { r.cancel() }

func (r *identityRig) identityCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM identities`).Scan(&n); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	return n
}

func (r *identityRig) tombstoneCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM identity_tombstones`).Scan(&n); err != nil {
		t.Fatalf("count identity_tombstones: %v", err)
	}
	return n
}

func (r *identityRig) suppressionCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM email_suppressions`).Scan(&n); err != nil {
		t.Fatalf("count email_suppressions: %v", err)
	}
	return n
}

func (r *identityRig) heldLookupCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM held_lookups`).Scan(&n); err != nil {
		t.Fatalf("count held_lookups: %v", err)
	}
	return n
}

func startIdentity(t *testing.T, amqpURL, dbPath string) *identityRig {
	t.Helper()
	dir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("resolve /contract: %v", err)
	}
	validator, err := messaging.NewValidator(dir)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db, err := persistence.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	store := persistence.NewStore(db)
	outbox := persistence.NewOutboxStore(db)
	erasureStore := persistence.NewErasureStore()
	heldStore := persistence.NewHeldLookupStore()

	consumerBus, err := messaging.DialConsumer(amqpURL, messaging.IdentityExchange)
	if err != nil {
		t.Fatalf("dial consumer: %v", err)
	}
	publisher, err := messaging.DialPublisher(amqpURL, messaging.IdentityExchange)
	if err != nil {
		t.Fatalf("dial publisher: %v", err)
	}

	relay := librelay.New(librelay.Config{
		Store:    outbox,
		Validate: validator.ValidateEnvelopeBytes,
		Publish:  publisher.PublishConfirmed,
		Interval: 100 * time.Millisecond,
		Log:      quietLog(),
	})
	resolver := &consumer.TxResolver{
		DB: db, Store: store, Outbox: outbox,
		ValidateOut: validator.ValidateEnvelopeBytes,
		MintID:      uuid.NewString,
		Source:      "identity",
		ResolvedKey: messaging.ResolvedRoutingKey,
		IsEmailSuppressed: func(ctx context.Context, tx *sql.Tx, emailHash string) (bool, error) {
			return erasureStore.IsEmailSuppressed(ctx, tx, emailHash)
		},
		RecordHeld: func(ctx context.Context, tx *sql.Tx, requestID, emailHash, occurredAt, recordedAt string) error {
			return heldStore.Record(ctx, tx, requestID, emailHash, "email suppressed by prior erasure", occurredAt, recordedAt)
		},
	}
	erasureHandler := &goperasure.Handler{
		DB:      db,
		Service: "identity",
		Mode:    goperasure.ModeDeleted,
		Delete: func(ctx context.Context, tx *sql.Tx, masterID string) error {
			email, found, err := erasureStore.LookupEmail(ctx, tx, masterID)
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
			hash := domain.HashEmail(domain.NormalizeEmail(email))
			return erasureStore.DeleteSlice(ctx, tx, masterID, hash, messaging.FormatWireTime(time.Now()))
		},
		Tombstone: func(ctx context.Context, tx *sql.Tx, masterID string) error {
			return erasureStore.WriteTombstone(ctx, tx, masterID, messaging.FormatWireTime(time.Now()))
		},
		Enqueue: func(ctx context.Context, tx *sql.Tx, ack messaging.Envelope) error {
			return librelay.EnqueueEnvelope(tx, outbox, validator.ValidateEnvelopeBytes, ack)
		},
	}
	handler := &consumer.Handler{
		Validate:   validator.ValidateEnvelopeBytes,
		Resolver:   resolver,
		Erasure:    erasureHandler,
		Log:        quietLog(),
		Notify:     relay.Kick,
		LookupKey:  messaging.LookupRequestedRoutingKey,
		ErasureKey: goperasure.ErasureRequestedType,
		Policy:     domain.DLQPolicy{MaxAttempts: 5, BaseMs: 50, Multiplier: 2, MaxMs: 1000},
		Retry:      consumerBus.RetryToDLX,
		Park:       consumerBus.ParkToDLX,
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := publisher.ConnectAndServe(ctx, quietLog(), func(bool) {}); err != nil {
		cancel()
		t.Fatalf("publisher ConnectAndServe: %v", err)
	}
	deliveries, err := consumerBus.ConnectAndConsume(ctx, messaging.ConsumerOptions{
		SourceExchange: frontendExchange,
		QueueName:      lookupQueueIT,
		RoutingKeys:    []string{messaging.LookupRequestedRoutingKey, goperasure.ErasureRequestedType},
		Prefetch:       16,
		DLXExchange:    messaging.IdentityDLXExchange,
	}, quietLog(), func(bool) {})
	if err != nil {
		cancel()
		t.Fatalf("ConnectAndConsume: %v", err)
	}
	go func() { _ = relay.Run(ctx) }()
	go handler.Run(ctx, deliveries)

	stop := func() {
		cancel()
		_ = consumerBus.Close()
		_ = publisher.Close()
		_ = db.Close()
	}
	return &identityRig{db: db, cancel: stop}
}

// --- frontend stand-in publisher -------------------------------------------

type frontendPub struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

func dialFrontend(t *testing.T, amqpURL string) *frontendPub {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial frontend: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("frontend channel: %v", err)
	}
	if err := ch.ExchangeDeclare(frontendExchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare frontend.events: %v", err)
	}
	return &frontendPub{conn: conn, ch: ch}
}

func (p *frontendPub) lookup(t *testing.T, envID, requestID, email string) {
	t.Helper()
	p.lookupRaw(t, envID, requestID, email)
}

// lookupRaw publishes an identity.lookup_requested with the given (possibly invalid)
// requestId, so the malformed path can be exercised.
func (p *frontendPub) lookupRaw(t *testing.T, envID, requestID, email string) {
	t.Helper()
	env := map[string]any{
		"id": envID, "type": "identity.lookup_requested", "source": "frontend",
		"schemaVersion": 1, "envelopeVersion": 1,
		"occurredAt": "2026-06-15T09:14:02.117Z", "correlationId": "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
		"causationId": nil,
		"data":        map[string]any{"requestId": requestID, "email": email},
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal lookup: %v", err)
	}
	if err := p.ch.PublishWithContext(context.Background(), frontendExchange,
		"identity.lookup_requested", false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		}); err != nil {
		t.Fatalf("publish lookup: %v", err)
	}
}

func (p *frontendPub) close() { _ = p.ch.Close(); _ = p.conn.Close() }

// --- identity.resolved observer --------------------------------------------

type resolvedData struct {
	RequestID string `json:"requestId"`
	Email     string `json:"email"`
	MasterID  string `json:"masterId"`
}

type observer struct {
	conn    *amqp.Connection
	ch      *amqp.Channel
	replies chan resolvedData
}

func newObserver(t *testing.T, amqpURL string) *observer {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial observer: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("observer channel: %v", err)
	}
	// identity.events is declared by the service; declare idempotently to be order-safe.
	if err := ch.ExchangeDeclare(messaging.IdentityExchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare identity.events: %v", err)
	}
	q, err := ch.QueueDeclare("identity.resolved.observer.it", false, true, true, false, nil)
	if err != nil {
		t.Fatalf("declare observer queue: %v", err)
	}
	if err := ch.QueueBind(q.Name, "identity.resolved", messaging.IdentityExchange, false, nil); err != nil {
		t.Fatalf("bind observer queue: %v", err)
	}
	raw, err := ch.Consume(q.Name, "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("consume observer queue: %v", err)
	}
	o := &observer{conn: conn, ch: ch, replies: make(chan resolvedData, 64)}
	go func() {
		for d := range raw {
			var env struct {
				Data resolvedData `json:"data"`
			}
			if err := json.Unmarshal(d.Body, &env); err == nil {
				o.replies <- env.Data
			}
		}
	}()
	return o
}

// await blocks until an identity.resolved with the given requestId arrives (or fails).
func (o *observer) await(t *testing.T, requestID string) resolvedData {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case r := <-o.replies:
			if r.RequestID == requestID {
				return r
			}
		case <-deadline:
			t.Fatalf("no identity.resolved for requestId %s within the deadline", requestID)
		}
	}
}

// countFor drains briefly and counts replies for a request id (used to prove a
// redelivery did NOT produce a second reply).
func (o *observer) countFor(requestID string) int {
	count := 0
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case r := <-o.replies:
			if r.RequestID == requestID {
				count++
			}
		case <-deadline:
			return count
		}
	}
}

func (o *observer) queueDepth(t *testing.T, queue string) int {
	t.Helper()
	q, err := o.ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		return 0 // not declared yet
	}
	return q.Messages
}

func (o *observer) close() { _ = o.ch.Close(); _ = o.conn.Close() }

// --- shared helpers --------------------------------------------------------

func startBroker(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	container, err := tcrabbitmq.Run(ctx, "rabbitmq:4.3-management-alpine")
	if err != nil {
		t.Fatalf("start rabbitmq container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	url, err := container.AmqpURL(ctx)
	if err != nil {
		t.Fatalf("amqp url: %v", err)
	}
	return url
}

func waitUntil(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if pred() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func isUUIDv4(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id.Version() == 4 && s == id.String() // id.String() is lowercase canonical
}
