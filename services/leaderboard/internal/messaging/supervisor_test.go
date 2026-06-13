package messaging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeConnector is a controllable connectFunc for unit-testing the supervisor
// without a broker. Each successful call hands out a fresh "closed" channel the
// test can close to simulate a dropped connection; the first failFirst calls
// return an error to exercise the backoff path.
type fakeConnector struct {
	mu        sync.Mutex
	calls     int
	failFirst int
	closeChs  []chan struct{}
	callSig   chan int // signals each call's 1-based index (buffered by the test)
}

func (f *fakeConnector) connect(context.Context) (<-chan struct{}, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if f.callSig != nil {
		f.callSig <- n
	}
	if n <= f.failFirst {
		return nil, errors.New("dial failed")
	}
	ch := make(chan struct{})
	f.mu.Lock()
	f.closeChs = append(f.closeChs, ch)
	f.mu.Unlock()
	return ch, nil
}

func (f *fakeConnector) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func recvState(t *testing.T, states <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-states:
		if got != want {
			t.Fatalf("state transition: got connected=%v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for state transition connected=%v", want)
	}
}

func recvCall(t *testing.T, calls <-chan int) int {
	t.Helper()
	select {
	case n := <-calls:
		return n
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for a connect() call")
		return 0
	}
}

func TestSupervisorConnectsOnceAndReportsConnected(t *testing.T) {
	fc := &fakeConnector{callSig: make(chan int, 8)}
	states := make(chan bool, 8)
	sup := newSupervisor(fc.connect, backoffPolicy{base: time.Millisecond, max: 10 * time.Millisecond},
		func(c bool) { states <- c }, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()

	recvCall(t, fc.callSig)    // dialed once on start
	recvState(t, states, true) // reported connected

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop on ctx cancel")
	}
	if c := fc.callCount(); c != 1 {
		t.Fatalf("expected exactly 1 connect call while healthy, got %d", c)
	}
}

func TestSupervisorReconnectsOnConnectionLoss(t *testing.T) {
	fc := &fakeConnector{callSig: make(chan int, 8)}
	states := make(chan bool, 8)
	sup := newSupervisor(fc.connect, backoffPolicy{base: time.Millisecond, max: 10 * time.Millisecond},
		func(c bool) { states <- c }, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sup.Run(ctx)

	recvCall(t, fc.callSig)
	recvState(t, states, true)

	// Drop the live connection -> supervisor must re-run connect (re-declares
	// topology) and report the lost->reconnected transition.
	fc.mu.Lock()
	close(fc.closeChs[0])
	fc.mu.Unlock()

	recvState(t, states, false) // saw the drop
	recvCall(t, fc.callSig)     // re-dialed (this IS the re-declare hook running again)
	recvState(t, states, true)  // back up
}

func TestSupervisorBacksOffThenConnects(t *testing.T) {
	fc := &fakeConnector{failFirst: 2, callSig: make(chan int, 8)}
	states := make(chan bool, 8)
	sup := newSupervisor(fc.connect, backoffPolicy{base: 4 * time.Millisecond, max: 8 * time.Millisecond},
		func(c bool) { states <- c }, discardLogger())

	var waits []time.Duration
	var wmu sync.Mutex
	sup.after = func(d time.Duration) <-chan time.Time {
		wmu.Lock()
		waits = append(waits, d)
		wmu.Unlock()
		ch := make(chan time.Time, 1)
		ch <- time.Time{} // fire immediately so the test never really sleeps
		return ch
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sup.Run(ctx)

	recvCall(t, fc.callSig) // attempt 1 (fails)
	recvCall(t, fc.callSig) // attempt 2 (fails)
	recvCall(t, fc.callSig) // attempt 3 (succeeds)
	recvState(t, states, true)

	wmu.Lock()
	defer wmu.Unlock()
	if len(waits) < 2 {
		t.Fatalf("expected at least 2 backoff waits, got %d (%v)", len(waits), waits)
	}
	// Capped exponential: base, then base*2 (clamped at max).
	if waits[0] != 4*time.Millisecond {
		t.Fatalf("first backoff: got %v, want 4ms", waits[0])
	}
	if waits[1] != 8*time.Millisecond {
		t.Fatalf("second backoff: got %v, want 8ms (capped)", waits[1])
	}
}

func TestSupervisorStopsOnCancelWhileBackingOff(t *testing.T) {
	fc := &fakeConnector{failFirst: 1000, callSig: make(chan int, 64)}
	sup := newSupervisor(fc.connect, backoffPolicy{base: time.Millisecond, max: time.Millisecond},
		func(bool) {}, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()

	recvCall(t, fc.callSig) // it is actively trying (and failing)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop while backing off")
	}
}
