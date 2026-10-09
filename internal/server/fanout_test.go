package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	integration "github.com/skyhook-io/radar/internal/integration"
)

func TestFanOutKeepsIndexOrderAndBoundsConcurrency(t *testing.T) {
	var running, peak atomic.Int32
	got := integration.FanOut(context.Background(), 20, 3, func(i int) int {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		running.Add(-1)
		return i * 10
	})
	if len(got) != 20 {
		t.Fatalf("len = %d", len(got))
	}
	for i, v := range got {
		if v != i*10 {
			t.Fatalf("results[%d] = %d, want %d", i, v, i*10)
		}
	}
	if p := peak.Load(); p > 3 || p < 1 {
		t.Errorf("peak concurrency = %d, want 1..3", p)
	}
}

func TestFanOutOfNothing(t *testing.T) {
	if got := integration.FanOut(context.Background(), 0, 4, func(int) string { t.Fatal("ran"); return "" }); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
