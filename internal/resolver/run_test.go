package resolver_test

import (
	"context"
	"testing"
	"time"

	"github.com/ericdahl-dev/app-green/internal/resolver"
)

// next waits for a snapshot or fails.
func next(t *testing.T, out <-chan resolver.Snapshot) resolver.Snapshot {
	t.Helper()
	select {
	case s := <-out:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no snapshot within 5s")
		return resolver.Snapshot{}
	}
}

// start runs h.r in the background and returns its channels and a stop
// function that waits for Run to return.
func start(t *testing.T, h *harness) (chan resolver.Snapshot, chan struct{}, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan resolver.Snapshot)
	refresh := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { defer close(done); h.r.Run(ctx, out, refresh) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return out, refresh, stop
}

func TestRunPollsNowThenOnRefresh(t *testing.T) {
	h := newHarness(t, nil) // default 60s interval: only refresh can poll again
	h.withShippedChain()
	out, refresh, _ := start(t, h)

	if s := next(t, out); len(s.Chains) != 1 {
		t.Fatalf("first snapshot chains = %d, want 1", len(s.Chains))
	}
	refresh <- struct{}{}
	next(t, out)
	if n, _ := h.tracker.calls(); n != 2 {
		t.Errorf("MyTickets calls = %d, want 2", n)
	}
}

func TestRunPollsEveryInterval(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	resolver.SetInterval(h.r, 10*time.Millisecond)
	out, _, _ := start(t, h)

	for range 3 {
		next(t, out)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	_, _, stop := start(t, h)
	// Nobody reads out: Run must still return when ctx is canceled.
	time.Sleep(20 * time.Millisecond)
	stop()
}

func TestPollsNeverOverlap(t *testing.T) {
	h := newHarness(t, nil)
	h.withShippedChain()
	release := make(chan struct{})
	h.tracker.set(func(f *fakeTracker) { f.block = release })
	out, refresh, _ := start(t, h)

	waitFor(t, func() bool { n, _ := h.tracker.calls(); return n == 1 })
	refresh <- struct{}{}
	polled := make(chan struct{})
	go func() { defer close(polled); h.r.Poll(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	if n, _ := h.tracker.calls(); n != 1 {
		t.Errorf("MyTickets calls while a poll runs = %d, want 1", n)
	}
	close(release)
	next(t, out)
	<-polled
	h.tracker.set(func(f *fakeTracker) {
		if f.maxFlight != 1 {
			t.Errorf("polls in flight at once = %d, want 1", f.maxFlight)
		}
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(time.Millisecond)
	}
}
