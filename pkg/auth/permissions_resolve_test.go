package auth

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
	pc := NewPermissionCache().WithConnectionKey(func() string { return "1" })
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
	pc := NewPermissionCache().WithConnectionKey(func() string { return "1" })
	cached := &UserPermissions{AllowedNamespaces: []string{"team-a"}}
	pc.Set("alice", nil, cached)
	var called bool
	perms, ok := pc.Resolve(context.Background(), "alice", nil, "1", func(context.Context) *UserPermissions {
		called = true
		return nil
	})
	if called || !ok || perms != cached {
		t.Fatalf("Resolve = %p, %v; want the cached entry", perms, ok)
	}
}

// A discovery that can't decide fails every caller sharing it and caches
// nothing, so the next caller asks again.
func TestResolve_FailureIsSharedAndNotCached(t *testing.T) {
	pc := NewPermissionCache().WithConnectionKey(func() string { return "1" })
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
	pc := NewPermissionCache().WithConnectionKey(func() string { return "1" })
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

// A caller that gives up doesn't fail the others waiting on the same run.
func TestResolve_CanceledCallerDoesNotFailOthers(t *testing.T) {
	pc := NewPermissionCache().WithConnectionKey(func() string { return "1" })
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
			var connection atomic.Value
			pc := NewPermissionCache().WithConnectionKey(func() string { return connection.Load().(string) })
			release := make(chan struct{})
			var calls atomic.Int32
			discover := func(context.Context) *UserPermissions {
				calls.Add(1)
				<-release
				return &UserPermissions{}
			}
			var wg sync.WaitGroup
			for _, c := range []caller{tc.a, tc.b} {
				connection.Store(c.connection)
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
	pc := NewPermissionCache().WithConnectionKey(func() string { return "1" })
	release := make(chan struct{})
	earlier := make(chan struct{})
	defer func() {
		close(release)
		<-earlier
	}()
	go func() {
		defer close(earlier)
		pc.Resolve(context.Background(), "alice", nil, "1", func(context.Context) *UserPermissions {
			<-release
			return &UserPermissions{AllowedNamespaces: []string{"stale"}}
		})
	}()
	waitForWaiters(t, pc, "alice", nil, "1", 1)
	pc.Invalidate()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	perms, ok := pc.Resolve(ctx, "alice", nil, "1", func(context.Context) *UserPermissions {
		return &UserPermissions{AllowedNamespaces: []string{"fresh"}}
	})
	if !ok || !slices.Equal(perms.AllowedNamespaces, []string{"fresh"}) {
		t.Fatal("the caller after Invalidate didn't get the discovery that began after it")
	}
}

func TestResolve_ExpiredEntryIsRediscovered(t *testing.T) {
	pc := NewPermissionCache().WithConnectionKey(func() string { return "1" })
	var calls atomic.Int32
	discover := func(context.Context) *UserPermissions {
		calls.Add(1)
		return &UserPermissions{}
	}
	pc.Resolve(context.Background(), "alice", nil, "1", discover)
	pc.Get("alice", nil).ExpiresAt = time.Now().Add(-time.Hour)
	pc.Resolve(context.Background(), "alice", nil, "1", discover)
	if c := calls.Load(); c != 2 {
		t.Fatalf("discoveries = %d, want a fresh one after the entry expired", c)
	}
}

// An entry cached while a discovery ran stays, with the verdicts recorded on
// it, and the discovery's callers get that entry.
func TestResolve_KeepsAnEntryCachedMeanwhile(t *testing.T) {
	pc := NewPermissionCache().WithConnectionKey(func() string { return "1" })
	release := make(chan struct{})
	result := make(chan *UserPermissions, 1)
	go func() {
		perms, _ := pc.Resolve(context.Background(), "alice", nil, "1", func(context.Context) *UserPermissions {
			<-release
			return &UserPermissions{AllowedNamespaces: []string{"team-a"}}
		})
		result <- perms
	}()
	waitForWaiters(t, pc, "alice", nil, "1", 1)
	meanwhile := &UserPermissions{AllowedNamespaces: []string{"team-a"}}
	pc.Set("alice", nil, meanwhile)
	meanwhile.SetCanI("list", "", "nodes", "", true)
	close(release)

	if got := <-result; got != meanwhile {
		t.Fatalf("the discovery's caller got %p, want the entry cached meanwhile %p", got, meanwhile)
	}
	cached := pc.Get("alice", nil)
	if cached != meanwhile {
		t.Fatal("the discovery replaced the entry cached meanwhile")
	}
	if v, ok := cached.CanI("list", "", "nodes", ""); !ok || !v {
		t.Fatal("the verdict recorded meanwhile was dropped")
	}
}

// A discovery that began before Invalidate answers its own callers but never
// replaces what was cached after it.
func TestResolve_DiscoveryDetachedByInvalidateIsNotCached(t *testing.T) {
	pc := NewPermissionCache().WithConnectionKey(func() string { return "1" })
	release := make(chan struct{})
	stale := make(chan *UserPermissions, 1)
	go func() {
		perms, _ := pc.Resolve(context.Background(), "alice", nil, "1", func(context.Context) *UserPermissions {
			<-release
			return &UserPermissions{AllowedNamespaces: nil}
		})
		stale <- perms
	}()
	waitForWaiters(t, pc, "alice", nil, "1", 1)
	pc.Invalidate()
	fresh, ok := pc.Resolve(context.Background(), "alice", nil, "1", func(context.Context) *UserPermissions {
		return &UserPermissions{AllowedNamespaces: []string{}}
	})
	if !ok {
		t.Fatal("the discovery after Invalidate failed")
	}
	fresh.SetCanI("list", "", "nodes", "", false)
	close(release)

	if got := <-stale; got == nil || got.AllowedNamespaces != nil {
		t.Fatal("the earlier caller didn't get its own discovery's answer")
	}
	cached := pc.Get("alice", nil)
	if cached != fresh {
		t.Fatal("the entry discovered after Invalidate was replaced")
	}
	if _, ok := cached.CanI("list", "", "nodes", ""); !ok {
		t.Fatal("the verdict recorded after Invalidate was dropped")
	}
}

func TestResolve_DiscoveryOutlivesItsLastCaller(t *testing.T) {
	for _, rejoin := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejoin=%v", rejoin), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pc := NewPermissionCache()
				var calls int
				release := make(chan struct{})
				answer := &UserPermissions{}
				discover := func(ctx context.Context) *UserPermissions {
					calls++
					select {
					case <-ctx.Done():
						return nil
					case <-release:
						return answer
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				go func() {
					if got, ok := pc.Resolve(ctx, "alice", nil, "", discover); got != nil || ok {
						t.Errorf("canceled caller got %p, %v", got, ok)
					}
				}()
				synctest.Wait()
				cancel()
				synctest.Wait()
				resolve := func() {
					if got, ok := pc.Resolve(t.Context(), "alice", nil, "", discover); !ok || got != answer {
						t.Errorf("got %p, %v; want original answer %p", got, ok, answer)
					}
				}
				if rejoin {
					go resolve()
					synctest.Wait()
				}
				close(release)
				synctest.Wait()
				if pc.Get("alice", nil) != answer {
					t.Fatal("discovery did not warm the cache")
				}
				resolve()
				if calls != 1 {
					t.Fatalf("discoveries = %d, want 1", calls)
				}
			})
		})
	}
}

func TestResolve_DiscoveryBudgetExpiresAndNextCallerRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pc := NewPermissionCache()
		if pc.discoveryTimeout != 2*time.Minute {
			t.Fatalf("default discovery timeout = %s", pc.discoveryTimeout)
		}
		pc.discoveryTimeout = 3 * time.Second
		start := time.Now()
		got, ok := pc.Resolve(t.Context(), "alice", nil, "", func(ctx context.Context) *UserPermissions {
			<-ctx.Done()
			return &UserPermissions{}
		})
		if elapsed := time.Since(start); elapsed != pc.discoveryTimeout || got != nil || ok || pc.Get("alice", nil) != nil {
			t.Fatalf("expired discovery returned %p, %v after %s", got, ok, elapsed)
		}
		answer := &UserPermissions{}
		got, ok = pc.Resolve(t.Context(), "alice", nil, "", func(context.Context) *UserPermissions { return answer })
		if !ok || got != answer || pc.Get("alice", nil) != answer {
			t.Fatal("next caller did not cache a fresh discovery")
		}
	})
}

func TestResolve_ConnectionChangeHidesWarmEntry(t *testing.T) {
	connection := "1"
	pc := NewPermissionCache().WithConnectionKey(func() string { return connection })
	first := &UserPermissions{}
	pc.Set("alice", nil, first)
	connection = "2"
	if pc.Get("alice", nil) != nil {
		t.Fatal("Get returned the old connection's entry")
	}
	second := &UserPermissions{AllowedNamespaces: []string{}}
	got, ok := pc.Resolve(t.Context(), "alice", nil, connection, func(context.Context) *UserPermissions { return second })
	if !ok || got != second || pc.Get("alice", nil) != second {
		t.Fatal("Resolve reused the old connection's entry")
	}
}

func TestResolve_ConnectionChangesAtPublication(t *testing.T) {
	pc := NewPermissionCache()
	discovered := false
	lockedAtPublish := false
	pc.WithConnectionKey(func() string {
		if discovered {
			lockedAtPublish = !pc.mu.TryRLock()
			if !lockedAtPublish {
				pc.mu.RUnlock()
			}
			return "2"
		}
		return "1"
	})
	got, ok := pc.Resolve(t.Context(), "alice", nil, "1", func(context.Context) *UserPermissions {
		discovered = true
		return &UserPermissions{}
	})
	if !lockedAtPublish || ok || got != nil || len(pc.cache) != 0 {
		t.Fatalf("publish lock held=%v, result=%p, %v, entries=%d", lockedAtPublish, got, ok, len(pc.cache))
	}
}

func TestResolve_AlreadyCanceledCallerFailsClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pc := NewPermissionCache()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var calls atomic.Int32
		for _, warm := range []bool{false, true} {
			if warm {
				pc.Set("alice", nil, &UserPermissions{})
			}
			got, ok := pc.Resolve(ctx, "alice", nil, "", func(context.Context) *UserPermissions {
				calls.Add(1)
				return &UserPermissions{}
			})
			synctest.Wait()
			if ok || got != nil || calls.Load() != 0 || len(pc.flights) != 0 {
				t.Fatalf("warm=%v: canceled caller got %p, %v or started discovery", warm, got, ok)
			}
		}
	})
}
