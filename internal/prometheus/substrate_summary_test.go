package prometheus

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/substrate"
)

// stubSubstrateCollection replaces the collectors with ones that count calls
// and concurrency, held until release is closed.
func stubSubstrateCollection(t *testing.T, release <-chan struct{}) (calls map[string]int, peak *atomic.Int32, mu *sync.Mutex) {
	t.Helper()
	calls, peak, mu = map[string]int{}, &atomic.Int32{}, &sync.Mutex{}
	var running atomic.Int32
	hold := func(key string) {
		mu.Lock()
		calls[key]++
		mu.Unlock()
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		running.Add(-1)
	}
	prevPool, prevNode, prevConn := substrateCollectPool, substrateCollectNode, substrateStillConnected
	substrateCollectPool = func(_ context.Context, _ *Client, p substrate.PoolRef) substrate.PoolService {
		hold("p:" + p.Key())
		return substrate.PoolService{Verdict: substrate.ServiceFreeActorSlots}
	}
	substrateCollectNode = func(_ context.Context, _ *Client, n string) substrate.NodeService {
		hold("n:" + n)
		return substrate.NodeService{Verdict: substrate.ServiceNoUploadsOverdue}
	}
	substrateStillConnected = func(*Client, uint64, uint64) bool { return true }
	t.Cleanup(func() {
		substrateCollectPool, substrateCollectNode, substrateStillConnected = prevPool, prevNode, prevConn
		substrateSummaryMu.Lock()
		substrateSummaryCache = map[string]substrateSubjectEntry{}
		substrateSummaryInFlight = map[string]bool{}
		substrateSummarySlots = map[string]chan struct{}{}
		substrateSummaryMu.Unlock()
	})
	return calls, peak, mu
}

func pools(names ...string) []substrate.PoolRef {
	out := make([]substrate.PoolRef, 0, len(names))
	for _, n := range names {
		out = append(out, substrate.PoolRef{Namespace: "ns", Name: n})
	}
	return out
}

func waitCollected(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		substrateSummaryMu.Lock()
		n := len(substrateSummaryInFlight)
		substrateSummaryMu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("collection did not finish")
}

// Callers with different grants share each subject's collection, are
// answered only with the subjects they asked for, and together never run
// more than the connection's bound.
func TestSubstrateServiceSharesSubjectsUnderOneBound(t *testing.T) {
	release := make(chan struct{})
	calls, peak, mu := stubSubstrateCollection(t, release)
	client := &Client{}

	var callers [][]substrate.PoolRef
	for i := range 8 {
		callers = append(callers, pools(fmt.Sprintf("p%d", i), fmt.Sprintf("p%d", i+1), "shared"))
	}
	for _, c := range callers {
		if svc := substrateServiceFor(client, 1, 1, c, nil); svc.Read || svc.Why != substrateCollecting || !svc.Collecting {
			t.Fatalf("before any answer = %+v", svc)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if got := peak.Load(); got > substrateSummaryParallel {
		t.Errorf("%d subjects collected at once, bound %d", got, substrateSummaryParallel)
	}
	close(release)
	waitCollected(t)
	mu.Lock()
	for key, n := range calls {
		if n != 1 {
			t.Errorf("%s collected %d times", key, n)
		}
	}
	mu.Unlock()

	svc := substrateServiceFor(client, 1, 1, pools("p0", "unknown"), []string{"node-a"})
	if !svc.Read || !slices.Equal(slices.Sorted(maps.Keys(svc.Pools)), []string{"ns/p0", "ns/unknown"}) {
		t.Fatalf("a caller gets exactly its subjects: %+v", svc)
	}
	if svc.Pools["ns/p0"].Verdict != substrate.ServiceFreeActorSlots || svc.Pools["ns/unknown"].Verdict != substrate.ServiceUnknown || svc.Nodes["node-a"].Verdict != substrate.ServiceUnknown {
		t.Errorf("collected, and not yet collected = %+v", svc)
	}
	if svc.ReadAt.IsZero() {
		t.Error("readAt is the oldest answer used")
	}
	// Another connection's answers are not reused.
	if other := substrateServiceFor(&Client{}, 1, 1, pools("p0"), nil); other.Read {
		t.Errorf("a new client reused the old one's answer: %+v", other)
	}
	waitCollected(t)
}
