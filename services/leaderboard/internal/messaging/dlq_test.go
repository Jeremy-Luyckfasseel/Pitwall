package messaging

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestParseRetryCount(t *testing.T) {
	cases := []struct {
		name    string
		headers amqp.Table
		want    int
	}{
		{"nil headers", nil, 0},
		{"absent", amqp.Table{"other": "x"}, 0},
		{"int32", amqp.Table{retryCountHeader: int32(3)}, 3},
		{"int64", amqp.Table{retryCountHeader: int64(4)}, 4},
		{"int", amqp.Table{retryCountHeader: 2}, 2},
		{"wrong type ignored", amqp.Table{retryCountHeader: "garbage"}, 0},
	}
	for _, c := range cases {
		if got := parseRetryCount(c.headers); got != c.want {
			t.Errorf("%s: parseRetryCount = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestAmqpDeliveryRetryCount(t *testing.T) {
	d := amqpDelivery{d: amqp.Delivery{Headers: amqp.Table{retryCountHeader: int32(7)}}}
	if got := d.RetryCount(); got != 7 {
		t.Errorf("RetryCount() = %d, want 7", got)
	}
	empty := amqpDelivery{d: amqp.Delivery{}}
	if got := empty.RetryCount(); got != 0 {
		t.Errorf("RetryCount() with no headers = %d, want 0", got)
	}
}

func TestBuildDLXPublishing_Retry(t *testing.T) {
	p := buildDLXPublishing([]byte(`{"x":1}`), 4000, 2, "")
	if p.Expiration != "4000" {
		t.Errorf("Expiration = %q, want \"4000\" (per-message TTL)", p.Expiration)
	}
	if p.DeliveryMode != amqp.Persistent {
		t.Errorf("DeliveryMode = %d, want persistent", p.DeliveryMode)
	}
	if got := parseRetryCount(p.Headers); got != 2 {
		t.Errorf("retry-count header = %d, want 2", got)
	}
	if _, ok := p.Headers[ParkReasonHeader]; ok {
		t.Error("a retry publish must not carry a park-reason header")
	}
}

func TestBuildDLXPublishing_Park(t *testing.T) {
	p := buildDLXPublishing([]byte(`{}`), 0, 0, "contract-invalid")
	if p.Expiration != "" {
		t.Errorf("park publish must have NO expiration, got %q", p.Expiration)
	}
	if p.Headers[ParkReasonHeader] != "contract-invalid" {
		t.Errorf("park-reason header = %v, want contract-invalid", p.Headers[ParkReasonHeader])
	}
}

func TestDLQQueueNames(t *testing.T) {
	if got := RetryQueueName("leaderboard.lap-recorded"); got != "leaderboard.lap-recorded.retry" {
		t.Errorf("RetryQueueName = %q", got)
	}
	if got := ParkingQueueName("leaderboard.lap-recorded"); got != "leaderboard.lap-recorded.parking" {
		t.Errorf("ParkingQueueName = %q", got)
	}
}
