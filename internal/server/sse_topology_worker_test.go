package server

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
)

// The topology worker exists so that a multi-second topology build never runs
// on the goroutine that drains the resource-change channel. These tests pin the
// two properties that make that true: requesting a broadcast never blocks the
// caller, and concurrent requests coalesce instead of queueing up builds.

// blockWorker occupies the topology worker with a build that only finishes when
// the returned release func is called, and reports when the worker has actually
// started. It stands in for a real large-cluster build without needing a census.
func blockWorker(t *testing.T, b *SSEBroadcaster) (release func()) {
	t.Helper()

	started := make(chan struct{})
	held := make(chan struct{})
	go func() {
		select {
		case <-b.topoTrigger:
			close(started)
			<-held
		case <-time.After(2 * time.Second):
			close(started)
		}
	}()

	b.requestTopologyBroadcast()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stand-in worker never picked up the trigger")
	}

	released := false
	return func() {
		if !released {
			released = true
			close(held)
		}
	}
}

func TestRequestTopologyBroadcastDoesNotBlockWhileBuildInFlight(t *testing.T) {
	b := NewSSEBroadcaster()
	release := blockWorker(t, b)
	defer release()

	// A build is now in flight. The change-consumer goroutine reaches this call
	// on every debounce fire; if it blocks here, changes stop being drained and
	// the resource-change channel overflows.
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		b.requestTopologyBroadcast()
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		// Generous bound: the point is "returns without waiting for the build",
		// not a latency budget. A blocking implementation waits indefinitely.
		if elapsed > 250*time.Millisecond {
			t.Errorf("requestTopologyBroadcast blocked for %v while a build was in flight; "+
				"the change consumer must never wait on a build", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("requestTopologyBroadcast blocked while a build was in flight; " +
			"the change consumer would stall and drop resource changes")
	}
}

// Every trigger source (debounce fire, context switch, CRD discovery,
// connection state) can fire while a build runs. However many arrive, the
// worker owes exactly one more build afterwards — not one per trigger, and not
// zero.
func TestConcurrentTopologyRequestsCoalesceIntoOneFollowUpBuild(t *testing.T) {
	b := NewSSEBroadcaster()

	var builds atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	b.topoBuild = func() bool {
		if builds.Add(1) == 1 {
			close(started)
			<-release
		}
		return true
	}

	go b.topologyWorker()
	t.Cleanup(b.Stop)

	b.requestTopologyBroadcast()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never started the first build")
	}

	for range 50 {
		b.requestTopologyBroadcast()
	}
	close(release)

	waitFor(t, 2*time.Second, func() bool { return builds.Load() >= 2 })

	// Give any surplus queued build time to run before declaring one.
	time.Sleep(100 * time.Millisecond)
	if got := builds.Load(); got != 2 {
		t.Errorf("builds = %d, want 2 (the one in flight plus a single coalesced follow-up)", got)
	}
}

// The worker consumes one token per cycle, and that token stands for every
// request that coalesced into it. Dropping it because the cluster view happens
// to be torn down strands the graph until something else triggers a build.
func TestTriggerConsumedWhileClusterViewIsDownIsRetried(t *testing.T) {
	b := NewSSEBroadcaster()

	var attempts atomic.Int64
	b.topoBuild = func() bool {
		attempts.Add(1)
		return false // cluster view unavailable
	}
	// The retry is skipped when nobody is watching, so register a client.
	go b.run()
	t.Cleanup(b.Stop)
	ch := b.Subscribe(nil, "resources", nil, nil)
	t.Cleanup(func() { b.Unsubscribe(ch) })
	waitFor(t, 2*time.Second, func() bool { return b.ClientCount() > 0 })

	go b.topologyWorker()

	b.requestTopologyBroadcast()
	waitFor(t, topologyRetryDelay*3, func() bool { return attempts.Load() >= 2 })
}

func waitFor(t *testing.T, limit time.Duration, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", limit)
}

// A cycle that started against the previous cluster must
// not reach clients, or the UI shows the old cluster's graph after it has
// already been told the context changed.
func TestBroadcastFromThePreviousClusterIsNotPublished(t *testing.T) {
	if k8s.GetResourceCache() == nil {
		t.Fatal("package fixture cache missing")
	}

	b := NewSSEBroadcaster()
	go b.run()
	t.Cleanup(b.Stop)

	ch := b.Subscribe(nil, "resources", nil, nil)
	t.Cleanup(func() { b.Unsubscribe(ch) })
	for range 100 {
		if b.ClientCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b.ClientCount() == 0 {
		t.Fatal("client never registered")
	}

	// Control: with no switch, this same cycle does publish. Without it the
	// assertion below would hold for a broadcaster that never publishes at all.
	b.broadcastTopologyUpdate()
	if !drainForTopologyFrame(ch) {
		t.Fatal("no topology frame published without a context switch; the test below would prove nothing")
	}

	// The switch lands the instant the cycle captures its epoch, which is the
	// worst case and the one a sleep can only approximate.
	b.topoEpochAtCycleStart = func() uint64 {
		epoch := b.topoEpoch.Load()
		b.topoEpoch.Add(1)
		return epoch
	}

	b.broadcastTopologyUpdate()

	if drainForTopologyFrame(ch) {
		t.Fatal("published a topology frame built against the previous cluster; " +
			"the UI would render it after context_changed")
	}
}

func TestSSETopologyBroadcastDuringAndAfterWarmup(t *testing.T) {
	b := NewSSEBroadcaster()
	go b.run()
	t.Cleanup(b.Stop)
	ch := b.Subscribe(nil, "resources", nil, nil)
	t.Cleanup(func() { b.Unsubscribe(ch) })
	waitFor(t, 2*time.Second, func() bool { return b.ClientCount() > 0 })

	for _, warming := range []bool{true, false} {
		if !warming {
			close(b.warmupDone)
		}
		if !b.broadcastTopologyUpdate() || !drainForTopologyFrame(ch) {
			t.Fatalf("no display topology frame with warming=%v", warming)
		}
	}
}

func drainForTopologyFrame(ch chan SSEEvent) bool {
	found := false
	for {
		select {
		case event := <-ch:
			if event.Event == "topology" {
				found = true
			}
		default:
			return found
		}
	}
}

func TestTopologyWorkerStopsWithBroadcaster(t *testing.T) {
	b := NewSSEBroadcaster()

	stopped := make(chan struct{})
	go func() {
		b.topologyWorker()
		close(stopped)
	}()

	b.Stop()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("topologyWorker did not exit after Stop; goroutine leaked")
	}
}
