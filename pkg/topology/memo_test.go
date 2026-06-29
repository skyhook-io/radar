package topology

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoizer_HitsAndMisses(t *testing.T) {
	m := NewMemoizer(1 * time.Second)
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"foo"}

	var calls int32
	build := func() (*Topology, error) {
		atomic.AddInt32(&calls, 1)
		return &Topology{}, nil
	}

	for i := range 5 {
		if _, err := m.Get(opts, build); err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected 1 build call (4 cache hits), got %d", got)
	}
}

func TestMemoizer_KeyDistinguishesOpts(t *testing.T) {
	m := NewMemoizer(1 * time.Second)
	var calls int32
	build := func() (*Topology, error) {
		atomic.AddInt32(&calls, 1)
		return &Topology{}, nil
	}

	a := DefaultBuildOptions()
	a.Namespaces = []string{"foo", "bar"}
	b := DefaultBuildOptions()
	b.Namespaces = []string{"foo", "baz"}

	if _, err := m.Get(a, build); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(b, build); err != nil {
		t.Fatal(err)
	}
	// Same opts as a but namespaces in different order — must hit the cache
	// because the key sorts namespaces. If callers pass the same set in
	// different order we must still treat it as the same query.
	aReordered := DefaultBuildOptions()
	aReordered.Namespaces = []string{"bar", "foo"}
	if _, err := m.Get(aReordered, build); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 builds (a, b; aReordered hits a), got %d", got)
	}
}

func TestMemoizer_TTLExpires(t *testing.T) {
	m := NewMemoizer(20 * time.Millisecond)
	var calls int32
	build := func() (*Topology, error) {
		atomic.AddInt32(&calls, 1)
		return &Topology{}, nil
	}
	opts := DefaultBuildOptions()

	if _, err := m.Get(opts, build); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := m.Get(opts, build); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 builds after TTL expiry, got %d", got)
	}
}

func TestMemoizer_ZeroTTLDisables(t *testing.T) {
	m := NewMemoizer(0)
	var calls int32
	build := func() (*Topology, error) {
		atomic.AddInt32(&calls, 1)
		return &Topology{}, nil
	}
	opts := DefaultBuildOptions()

	for range 3 {
		if _, err := m.Get(opts, build); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 builds with TTL=0, got %d", got)
	}
}

func TestMemoizer_ConcurrentColdReadsBuildOnce(t *testing.T) {
	// The page-load case: /tree and /insights fire simultaneously on a cold
	// cache. Without singleflight, both would walk the informers; with it,
	// only one build runs and the rest receive the cached result.
	m := NewMemoizer(1 * time.Second)
	var calls int32
	start := make(chan struct{})
	build := func() (*Topology, error) {
		atomic.AddInt32(&calls, 1)
		// Hold the build open long enough that all goroutines pile up
		// at the singleflight gate before the first one stores.
		time.Sleep(20 * time.Millisecond)
		return &Topology{}, nil
	}
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"foo"}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			<-start
			if _, err := m.Get(opts, build); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected 1 build under %d concurrent cold reads, got %d", n, got)
	}
}

// TestMemoizer_EvictsStaleEntries pins the bounded-map invariant. store()
// walks the map on every write and drops entries older than 2× the TTL.
// Without this, a Radar process whose namespace filter rotates (or whose
// users hit many distinct paths over its lifetime) would accumulate
// Topology pointers indefinitely — invisible in short-lived dev binaries,
// a memory leak in long-running in-cluster deployments.
func TestMemoizer_EvictsStaleEntries(t *testing.T) {
	ttl := 20 * time.Millisecond
	m := NewMemoizer(ttl)
	build := func() (*Topology, error) { return &Topology{}, nil }

	// Seed 5 distinct keys.
	for i := range 5 {
		opts := DefaultBuildOptions()
		opts.Namespaces = []string{"ns-" + string(rune('a'+i))}
		if _, err := m.Get(opts, build); err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
	}
	// Wait past 2× TTL so existing entries are eligible for eviction.
	time.Sleep(3 * ttl)
	// One more write triggers the eviction walk.
	final := DefaultBuildOptions()
	final.Namespaces = []string{"keep"}
	if _, err := m.Get(final, build); err != nil {
		t.Fatalf("final Get: %v", err)
	}

	m.mu.Lock()
	got := len(m.entries)
	m.mu.Unlock()
	// We expect only the one fresh entry — the 5 seed entries were past
	// 2× TTL when the final write ran.
	if got > 1 {
		t.Errorf("expected ≤1 entry after eviction, got %d", got)
	}
}

// TestMemoizer_GetIndex_CachesAlongsideTopology pins that GetIndex returns
// the same *RelationshipsIndex across calls for the same key, proving the
// lazy index is cached on the memoEntry (not rebuilt per call). T6/T12
// hot paths depend on this: they call Get + GetIndex on every per-resource
// request.
func TestMemoizer_GetIndex_CachesAlongsideTopology(t *testing.T) {
	m := NewMemoizer(1 * time.Second)
	topo := &Topology{
		Nodes: []Node{{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"}},
		Edges: []Edge{},
	}
	build := func() (*Topology, error) { return topo, nil }
	opts := DefaultBuildOptions()

	idx1, err := m.GetIndex(opts, build)
	if err != nil {
		t.Fatalf("GetIndex 1: %v", err)
	}
	idx2, err := m.GetIndex(opts, build)
	if err != nil {
		t.Fatalf("GetIndex 2: %v", err)
	}
	if idx1 != idx2 {
		t.Errorf("expected same *RelationshipsIndex across calls (cached), got two distinct pointers")
	}
}

// TestMemoizer_GetIndex_ZeroTTL_BuildsInline mirrors TestMemoizer_ZeroTTLDisables:
// with caching disabled, every GetIndex call must produce a fresh index.
// Required for tests that drive Memoizer with ttl=0.
func TestMemoizer_GetIndex_ZeroTTL_BuildsInline(t *testing.T) {
	m := NewMemoizer(0)
	topo := &Topology{
		Nodes: []Node{{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"}},
		Edges: []Edge{},
	}
	build := func() (*Topology, error) { return topo, nil }
	opts := DefaultBuildOptions()

	idx1, err := m.GetIndex(opts, build)
	if err != nil {
		t.Fatalf("GetIndex 1: %v", err)
	}
	idx2, err := m.GetIndex(opts, build)
	if err != nil {
		t.Fatalf("GetIndex 2: %v", err)
	}
	if idx1 == idx2 {
		t.Errorf("ttl=0 should rebuild index inline each call, got identical pointers")
	}
}

func TestMemoizer_DoesNotCacheErrors(t *testing.T) {
	m := NewMemoizer(1 * time.Second)
	var calls int32
	wantErr := errors.New("boom")
	build := func() (*Topology, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return nil, wantErr
		}
		return &Topology{}, nil
	}
	opts := DefaultBuildOptions()

	if _, err := m.Get(opts, build); err == nil {
		t.Fatal("expected error on first Get")
	}
	// Second call should re-invoke build (errors aren't cached) and succeed.
	if _, err := m.Get(opts, build); err != nil {
		t.Fatalf("expected success on second Get, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 build calls, got %d", got)
	}
}
