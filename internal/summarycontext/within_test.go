package summarycontext

import (
	"sync/atomic"
	"testing"
	"time"
)

// Search must not wait on a cold build of its per-hit context: after a
// restart it reads custom resources that wait on custom-resource discovery
// for tens of seconds. While one such build is still stuck, later searches
// don't start another.
func TestWithinDoesNotWaitPastBudgetOrPileUp(t *testing.T) {
	release := make(chan struct{})
	var builds atomic.Int32
	stuck := func() *int {
		builds.Add(1)
		<-release
		one := 1
		return &one
	}
	start := time.Now()
	if got := Within(50*time.Millisecond, stuck); got != nil || time.Since(start) > time.Second {
		t.Fatalf("got %v after %s; want nil within the budget", got, time.Since(start))
	}
	start = time.Now()
	if got := Within(time.Second, stuck); got != nil || time.Since(start) > 100*time.Millisecond || builds.Load() != 1 {
		t.Fatalf("second call waited %s and started %d builds; want an immediate nil and one build", time.Since(start), builds.Load())
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for lagging.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := Within(time.Second, func() int { return 7 }); got != 7 {
		t.Fatalf("a result ready in time was dropped once the stuck build finished: %d", got)
	}
}
