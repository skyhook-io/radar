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
