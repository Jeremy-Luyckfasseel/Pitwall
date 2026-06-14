package messaging

import (
	"strings"
	"testing"
)

// DeclareDLQTopology must fail fast (before touching the broker) when DLXExchange is
// empty — the per-service DLX name is required now that it is no longer a constant.
func TestDeclareDLQTopology_RequiresDLXExchange(t *testing.T) {
	b := &Bus{} // no connection needed: the guard runs before curCh()
	err := b.DeclareDLQTopology(ConsumerOptions{
		SourceExchange: "timing.events",
		QueueName:      "consumer.work",
		RoutingKeys:    []string{"lap.recorded"},
		Prefetch:       16,
		// DLXExchange intentionally omitted
	})
	if err == nil {
		t.Fatal("expected an error when DLXExchange is empty")
	}
	if !strings.Contains(err.Error(), "DLXExchange") {
		t.Fatalf("error should name the missing field, got: %v", err)
	}
}
