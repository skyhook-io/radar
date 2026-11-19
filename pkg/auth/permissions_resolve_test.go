package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitForWaiters blocks until n callers wait on the identity's run.
func waitForWaiters(t *testing.T, pc *PermissionCache, username string, groups []string, connection string, n int) {
	t.Helper()
	flightKey := string(appendKeyPart([]byte(cacheKey(username, groups)), connection))
	deadline := time.Now().Add(5 * time.Second)
	for {
		pc.mu.Lock()
		f := pc.flights[flightKey]
		waiting := 0
		if f != nil {
			waiting = f.waiters
		}
		pc.mu.Unlock()
		if waiting == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d callers waiting, want %d", waiting, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// Callers that miss at once share one discovery and get the one entry it
// cached.
func TestResolve_ConcurrentMissesShareOneDiscovery(t *testing.T) {
	pc := NewPermissionCache()
	release := make(chan struct{})
	var calls atomic.Int32
	discover := func(context.Context) *UserPermissions {
		calls.Add(1)
		<-release
		return &UserPermissions{AllowedNamespaces: []string{"team-a"}}
	}
	const n = 8
	got := make([]*UserPermissions, n)
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			perms, ok := pc.Resolve(context.Background(), "alice", []string{"dev"}, "1", discover)
			if !ok {
				t.Errorf("caller %d failed", i)
			}
			got[i] = perms
		}()
	}
	waitForWaiters(t, pc, "alice", []string{"dev"}, "1", n)
	close(release)
	wg.Wait()
	if c := calls.Load(); c != 1 {
		t.Fatalf("discoveries = %d, want 1", c)
	}
	cached := pc.Get("alice", []string{"dev"})
	for i, perms := range got {
		if perms != cached {
			t.Fatalf("caller %d got %p, want the cached entry %p", i, perms, cached)
		}
	}
}

func TestResolve_ServesALiveEntryWithoutDiscovering(t *testing.T) {
	pc := NewPermissionCache()
	cached := &UserPermissions{AllowedNamespaces: []string{"team-a"}}
	pc.Set("alice", nil, cached)
	perms, ok := pc.Resolve(context.Background(), "alice", nil, "1", func(context.Context) *UserPermissions {
		t.Fatal("discovered with a live entry cached")
		return nil
	})
	if !ok || perms != cached {
		t.Fatalf("Resolve = %p, %v; want the cached entry", perms, ok)
	}
}

// A discovery that can't decide fails every caller sharing it and caches
// nothing, so the next caller asks again.
func TestResolve_FailureIsSharedAndNotCached(t *testing.T) {
	pc := NewPermissionCache()
	release := make(chan struct{})
	var calls atomic.Int32
	discover := func(context.Context) *UserPermissions {
		if calls.Add(1) == 1 {
			<-release
		}
		return nil
	}
	const n = 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if perms, ok := pc.Resolve(context.Background(), "alice", nil, "1", discover); ok || perms != nil {
				t.Errorf("Resolve = %v, %v; want a failure", perms, ok)
			}
		}()
	}
	waitForWaiters(t, pc, "alice", nil, "1", n)
	close(release)
	wg.Wait()
	if pc.Get("alice", nil) != nil {
		t.Fatal("a failed discovery was cached")
	}
	pc.Resolve(context.Background(), "alice", nil, "1", discover)
	if c := calls.Load(); c != 2 {
		t.Fatalf("discoveries = %d, want a fresh one after the failure", c)
	}
}

func TestResolve_PanicFailsClosed(t *testing.T) {
	pc := NewPermissionCache()
	perms, ok := pc.Resolve(context.Background(), "alice", nil, "1", func(context.Context) *UserPermissions {
		panic("boom")
	})
	if ok || perms != nil {
		t.Fatalf("Resolve = %v, %v; want a failure", perms, ok)
	}
	if pc.Get("alice", nil) != nil {
		t.Fatal("a panicked discovery was cached")
	}
}

// The run ends when no caller waits for it, as a caller's own discovery did.
func TestResolve_LastCallerLeavingCancelsTheRun(t *testing.T) {
	pc := NewPermissionCache()
	canceled := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	perms, ok := pc.Resolve(ctx, "alice", nil, "1", func(runCtx context.Context) *UserPermissions {
		<-runCtx.Done()
		close(canceled)
		return nil
	})
	if ok || perms != nil {
		t.Fatalf("Resolve = %v, %v; want a failure", perms, ok)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the run kept going after its only caller left")
	}
}

// A caller that gives up doesn't fail the others waiting on the same run.
func TestResolve_CanceledCallerDoesNotFailOthers(t *testing.T) {
	pc := NewPermissionCache()
	release := make(chan struct{})
	var calls atomic.Int32
	discover := func(runCtx context.Context) *UserPermissions {
		calls.Add(1)
		select {
		case <-release:
			return &UserPermissions{}
		case <-runCtx.Done():
			return nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan bool, 1)
	go func() {
		_, ok := pc.Resolve(ctx, "alice", nil, "1", discover)
		first <- ok
	}()
	waitForWaiters(t, pc, "alice", nil, "1", 1)
	second := make(chan bool, 1)
	go func() {
		_, ok := pc.Resolve(context.Background(), "alice", nil, "1", discover)
		second <- ok
	}()
	waitForWaiters(t, pc, "alice", nil, "1", 2)
	cancel()
	if <-first {
		t.Fatal("the caller that gave up got an answer")
	}
	close(release)
	if !<-second {
		t.Fatal("the remaining caller failed when another gave up")
	}
	if c := calls.Load(); c != 1 {
		t.Fatalf("discoveries = %d, want 1", c)
	}
}

// Callers on different connections, or with identities that differ only in
// where a separator byte falls, never share a run.
func TestResolve_DistinctConnectionsAndIdentitiesDoNotShare(t *testing.T) {
	type caller struct {
		user       string
		groups     []string
		connection string
	}
	for _, tc := range []struct {
		name string
		a, b caller
	}{
		{"connection", caller{"alice", nil, "1"}, caller{"alice", nil, "2"}},
		{"identity", caller{"alice", []string{"team\x00prod"}, "1"}, caller{"alice\x00team", []string{"prod"}, "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pc := NewPermissionCache()
			release := make(chan struct{})
			var calls atomic.Int32
			discover := func(context.Context) *UserPermissions {
				calls.Add(1)
				<-release
				return &UserPermissions{}
			}
			var wg sync.WaitGroup
			for _, c := range []caller{tc.a, tc.b} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					pc.Resolve(context.Background(), c.user, c.groups, c.connection, discover)
				}()
				waitForWaiters(t, pc, c.user, c.groups, c.connection, 1)
			}
			close(release)
			wg.Wait()
			if c := calls.Load(); c != 2 {
				t.Fatalf("discoveries = %d, want one each", c)
			}
		})
	}
}

// A caller after Invalidate never joins a discovery that began before it.
func TestResolve_InvalidateStartsANewDiscovery(t *testing.T) {
	pc := NewPermissionCache()
	release := make(chan struct{})
	defer close(release)
	go pc.Resolve(context.Background(), "alice", nil, "1", func(context.Context) *UserPermissions {
		<-release
		return &UserPermissions{AllowedNamespaces: []string{"stale"}}
	})
	waitForWaiters(t, pc, "alice", nil, "1", 1)
	pc.Invalidate()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	perms, ok := pc.Resolve(ctx, "alice", nil, "1", func(context.Context) *UserPermissions {
		return &UserPermissions{AllowedNamespaces: []string{"fresh"}}
	})
	if !ok || len(perms.AllowedNamespaces) != 1 || perms.AllowedNamespaces[0] != "fresh" {
		t.Fatalf("Resolve = %+v, %v; want the discovery that began after Invalidate", perms, ok)
	}
}

func TestResolve_ExpiredEntryIsRediscovered(t *testing.T) {
	pc := &PermissionCache{cache: make(map[string]*UserPermissions), ttl: time.Millisecond}
	var calls atomic.Int32
	discover := func(context.Context) *UserPermissions {
		calls.Add(1)
		return &UserPermissions{}
	}
	pc.Resolve(context.Background(), "alice", nil, "1", discover)
	time.Sleep(5 * time.Millisecond)
	pc.Resolve(context.Background(), "alice", nil, "1", discover)
	if c := calls.Load(); c != 2 {
		t.Fatalf("discoveries = %d, want a fresh one after the entry expired", c)
	}
}
