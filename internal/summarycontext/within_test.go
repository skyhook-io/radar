package summarycontext

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithinContextBoundsAbandonedBuildsAndKeepsSourcesIndependent(t *testing.T) {
	gate := NewBudgetGate(2)
	release := make(chan struct{})
	var builds atomic.Int32
	stuck := func() int { builds.Add(1); <-release; return 1 }
	for range 6 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		got := WithinContext(ctx, gate, stuck)
		cancel()
		if got != 0 {
			t.Fatalf("expired build = %d", got)
		}
	}
	if builds.Load() != 2 {
		t.Fatalf("started %d builds, want only two", builds.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got := WithinContext(ctx, NewBudgetGate(1), func() int { return 7 }); got != 7 {
		t.Fatal("a cold source suppressed a different source")
	}
	close(release)
	if got := WithinContext(ctx, gate, func() int { return 7 }); got != 7 {
		t.Fatal("released capacity did not admit the next request")
	}
}

func TestWithinContextCancellationDoesNotStartWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := WithinContext(ctx, NewBudgetGate(1), func() int { t.Error("started after cancellation"); return 7 })
	if got != 0 {
		t.Fatalf("cancelled result = %d", got)
	}
}

func TestWithinContextRecoversAndReleasesCapacity(t *testing.T) {
	gate := NewBudgetGate(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got := WithinContext(ctx, gate, func() int { panic("invalid cached shape") }); got != 0 {
		t.Fatalf("panicking enrichment returned %d", got)
	}
	if got := WithinContext(ctx, gate, func() int { return 7 }); got != 7 {
		t.Fatal("panicking enrichment retained its slot")
	}
}
