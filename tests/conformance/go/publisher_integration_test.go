//go:build integration

package conformance

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// timingExchange is the source exchange the Leaderboard binds to (TIMING_EXCHANGE
// default). For the consumer-focused scenarios the harness ITSELF is the producer,
// publishing schema-valid lap.recorded / session.started envelopes here.
const timingExchange = "timing.events"

const (
	routingLapRecorded    = "lap.recorded"
	routingSessionStarted = "session.started"
)

// harnessPublisher publishes deterministic, /contract-valid envelopes to the
// timing.events exchange as a stand-in producer (the SUT is the real Leaderboard).
type harnessPublisher struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

func dialPublisher(t *testing.T, amqpURL string) *harnessPublisher {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("publisher dial: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("publisher channel: %v", err)
	}
	// Declare the SAME durable topic exchange the real Timing producer declares
	// (publisher.go: topic, durable, non-auto-delete) so params match.
	if err := ch.ExchangeDeclare(timingExchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare %s: %v", timingExchange, err)
	}
	// Confirm mode: publish() waits for a broker ack, so a persistent message is
	// durably persisted before we return — required by publish-redeliver, which
	// bounces the broker right after publishing (the messages must survive the restart).
	if err := ch.Confirm(false); err != nil {
		t.Fatalf("enable publisher confirms: %v", err)
	}
	p := &harnessPublisher{conn: conn, ch: ch}
	t.Cleanup(p.close)
	return p
}

func (p *harnessPublisher) close() {
	if p.ch != nil {
		_ = p.ch.Close()
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
}

// envelope is the standard wire envelope (camelCase, all fields present — AR7).
type envelope struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	Source          string         `json:"source"`
	SchemaVersion   int            `json:"schemaVersion"`
	EnvelopeVersion int            `json:"envelopeVersion"`
	OccurredAt      string         `json:"occurredAt"`
	CorrelationID   string         `json:"correlationId"`
	CausationID     *string        `json:"causationId"`
	Data            map[string]any `json:"data"`
}

func nowMillis() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func (p *harnessPublisher) publish(t *testing.T, routingKey string, env envelope) {
	t.Helper()
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dc, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, timingExchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
	if err != nil {
		t.Fatalf("publish %s: %v", routingKey, err)
	}
	// Wait for the broker confirm so the persistent message is durably stored.
	select {
	case <-dc.Done():
		if !dc.Acked() {
			t.Fatalf("publish %s was nacked by the broker", routingKey)
		}
	case <-ctx.Done():
		t.Fatalf("publish %s confirm timed out: %v", routingKey, ctx.Err())
	}
}

// sessionStarted publishes a session.started so the board has a live session.
func (p *harnessPublisher) sessionStarted(t *testing.T, session string) {
	t.Helper()
	at := nowMillis()
	p.publish(t, routingSessionStarted, envelope{
		ID:              newUUID(t),
		Type:            "session.started",
		Source:          "timing",
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      at,
		CorrelationID:   newUUID(t),
		Data:            map[string]any{"sessionId": session, "startedAt": at},
	})
}

// lap publishes a lap.recorded with an explicit envelope id (so a duplicate can
// reuse the id) — the idempotency key is the envelope id (M6).
func (p *harnessPublisher) lap(t *testing.T, envelopeID, master, session string, lapNumber int, lapMs int64) {
	t.Helper()
	p.publish(t, routingLapRecorded, envelope{
		ID:              envelopeID,
		Type:            "lap.recorded",
		Source:          "timing",
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		OccurredAt:      nowMillis(),
		CorrelationID:   newUUID(t),
		Data: map[string]any{
			"masterId":  master,
			"sessionId": session,
			"lapNumber": lapNumber,
			"lapTimeMs": lapMs,
			"at":        nowMillis(),
		},
	})
}

func newUUID(t *testing.T) string {
	t.Helper()
	// Lowercase UUID v4 (envelope id). Use crypto/rand via fmt for a valid v4.
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("uuid rand: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
