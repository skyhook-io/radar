package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// slowHelmRead stands in for a Helm storage read that takes as long as the
// test says, counting how many reads were started.
type slowHelmRead struct {
	reads   atomic.Int32
	release chan struct{}
	stopped chan struct{}
}

func newSlowHelmRead() *slowHelmRead {
	return &slowHelmRead{release: make(chan struct{}), stopped: make(chan struct{}, 64)}
}

func (r *slowHelmRead) read(ctx context.Context) ([]issues.Issue, error) {
	r.reads.Add(1)
	select {
	case <-r.release:
		return []issues.Issue{{Kind: "HelmRelease", Name: "api"}}, nil
	case <-ctx.Done():
		r.stopped <- struct{}{}
		return nil, ctx.Err()
	}
}

func (r *slowHelmRead) waitReads(t *testing.T, n int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.reads.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("reads = %d, want %d", r.reads.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func finishedHelmRead() *slowHelmRead {
	r := newSlowHelmRead()
	close(r.release)
	return r
}

func failingHelmRead(err error) helmIssuesRead {
	return func(context.Context) ([]issues.Issue, error) { return nil, err }
}

// helmWait is how long a test caller waits on a running read.
func helmWait(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func newTestHelmIssuesCache(t *testing.T) *helmIssuesCache {
	c := newHelmIssuesCache()
	t.Cleanup(c.invalidate)
	return c
}

// ageEntry makes key's result look older than the refresh age.
func ageEntry(c *helmIssuesCache, key string, by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key].last.at = c.entries[key].last.at.Add(-by)
}

func waitIdle(t *testing.T, c *helmIssuesCache, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		e := c.entries[key]
		busy := e != nil && e.done != nil
		c.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("read never finished")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHelmIssuesCacheSlowFirstReadConverges(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	read := newSlowHelmRead()

	view := cache.get(helmWait(t, 20*time.Millisecond), "alice", read.read)
	if view.snapshot != nil || !view.reading || view.failure != "" {
		t.Fatalf("first poll = %+v, want not checked yet, read running", view)
	}
	close(read.release)
	waitIdle(t, cache, "alice")
	view = cache.get(helmWait(t, time.Second), "alice", read.read)
	if view.snapshot == nil || len(view.snapshot.issues) != 1 || view.failure != "" {
		t.Fatalf("after the read finished = %+v, want its result", view)
	}
	if got := read.reads.Load(); got != 1 {
		t.Fatalf("reads = %d, want 1", got)
	}
}

// Stale-while-revalidate: with an earlier result, a poll answers at once and
// refreshes in the background, however long that refresh takes.
func TestHelmIssuesCacheAnswersWithEarlierResultWhileRefreshing(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	cache.get(helmWait(t, time.Second), "alice", finishedHelmRead().read)
	ageEntry(cache, "alice", time.Hour)

	slow := newSlowHelmRead()
	started := time.Now()
	view := cache.get(helmWait(t, 5*time.Second), "alice", slow.read)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("waited %s with a result on hand", elapsed)
	}
	if view.snapshot == nil || !view.reading || view.failure != "" {
		t.Fatalf("view = %+v, want the earlier result with a refresh running", view)
	}
	slow.waitReads(t, 1)
}

func TestHelmIssuesCacheDoesNotRefreshAFreshResult(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	first := finishedHelmRead()
	cache.get(helmWait(t, time.Second), "alice", first.read)
	second := newSlowHelmRead()
	view := cache.get(helmWait(t, time.Second), "alice", second.read)
	if view.reading || second.reads.Load() != 0 {
		t.Fatalf("a result younger than %s started another read", helmIssuesRefreshAfter)
	}
}

func TestHelmIssuesCacheRunsOneReadAtATime(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	read := newSlowHelmRead()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.get(helmWait(t, 10*time.Millisecond), "alice", read.read)
		}()
	}
	wg.Wait()
	read.waitReads(t, 1)
	time.Sleep(20 * time.Millisecond)
	if got := read.reads.Load(); got != 1 {
		t.Fatalf("reads = %d, want 1 for 20 concurrent polls", got)
	}
	cache.get(helmWait(t, 0), "bob", read.read)
	read.waitReads(t, 2) // another identity or scope is another read
}

func TestHelmIssuesCacheCallerLeavingStopsOnlyItsWait(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	read := newSlowHelmRead()
	caller, leave := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		cache.get(caller, "alice", read.read)
		close(returned)
	}()
	read.waitReads(t, 1)
	leave()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("a caller that left kept waiting on the read")
	}
	select {
	case <-read.stopped:
		t.Fatal("the caller leaving stopped the shared read")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHelmIssuesCacheReportsAReadPastTheCapAsFailed(t *testing.T) {
	previous := helmIssuesReadTimeout
	helmIssuesReadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { helmIssuesReadTimeout = previous })
	cache := newTestHelmIssuesCache(t)
	read := newSlowHelmRead()

	cache.get(helmWait(t, 0), "alice", read.read)
	select {
	case <-read.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("a read ran past helmIssuesReadTimeout")
	}
	waitIdle(t, cache, "alice")
	cache.mu.Lock()
	failure := cache.entries["alice"].failure
	cache.mu.Unlock()
	if failure != helmReadTimedOut {
		t.Fatalf("failure = %q, want %q", failure, helmReadTimedOut)
	}
}

func TestHelmIssuesCacheFailedRefreshIsNotCurrent(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	cache.get(helmWait(t, time.Second), "alice", finishedHelmRead().read)
	ageEntry(cache, "alice", time.Hour)

	cache.get(helmWait(t, time.Second), "alice", failingHelmRead(errors.New("apiserver unavailable")))
	waitIdle(t, cache, "alice")
	view := cache.get(helmWait(t, 0), "alice", newSlowHelmRead().read)
	if view.snapshot == nil || view.failure == "" {
		t.Fatalf("view = %+v, want the older result with the failure", view)
	}
}

// A 403 is a failed read with its reason, never an empty success.
func TestHelmIssuesCacheDeniedReadIsAFailure(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("denied"))
	view := cache.get(helmWait(t, time.Second), "alice", failingHelmRead(fmt.Errorf("failed to inspect release storage namespaces: %w", denied)))
	if view.snapshot != nil || view.failure != helmReadForbidden {
		t.Fatalf("view = %+v, want a denied failure and no result", view)
	}
}

func TestHelmIssuesCacheClearsOnContextSwitch(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	cache.get(helmWait(t, time.Second), "alice", finishedHelmRead().read)
	running := newSlowHelmRead()
	cache.get(helmWait(t, 0), "bob", running.read)
	running.waitReads(t, 1)

	cache.invalidate()

	select {
	case <-running.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("a read from before the switch kept running")
	}
	fresh := newSlowHelmRead()
	if view := cache.get(helmWait(t, 0), "alice", fresh.read); view.snapshot != nil {
		t.Fatalf("result from before the switch survived: %+v", view)
	}
	fresh.waitReads(t, 1)
}

func TestHelmIssuesCacheWaiterDropsResultsFromBeforeASwitch(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	running := newSlowHelmRead()
	result := make(chan helmIssuesView, 1)
	go func() { result <- cache.get(helmWait(t, 5*time.Second), "alice", running.read) }()
	running.waitReads(t, 1)
	cache.invalidate()
	select {
	case view := <-result:
		if view.snapshot != nil || view.reading || view.failure != "" {
			t.Fatalf("waiter returned pre-switch state: %+v", view)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not return after the switch")
	}
}

func TestHelmIssuesCacheHooksClearIt(t *testing.T) {
	var hooks []k8s.ContextSwitchCallback
	register := func(callback k8s.ContextSwitchCallback) { hooks = append(hooks, callback) }
	cache := newTestHelmIssuesCache(t)
	cache.invalidateOn(register, register)
	if len(hooks) != 2 {
		t.Fatalf("registered %d hooks, want 2", len(hooks))
	}
	for i, hook := range hooks {
		cache.get(helmWait(t, time.Second), "alice", finishedHelmRead().read)
		hook("other-context")
		cache.mu.Lock()
		_, kept := cache.entries["alice"]
		cache.mu.Unlock()
		if kept {
			t.Fatalf("hook %d left cached Helm issues in place", i)
		}
	}
}

func TestHelmIssuesCacheEvictsIdleScopes(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	now := time.Now()
	cache.now = func() time.Time { return now }
	cache.get(helmWait(t, time.Second), "alice", finishedHelmRead().read)
	waitIdle(t, cache, "alice")

	now = now.Add(helmIssuesEntryIdle + time.Minute)
	cache.get(helmWait(t, time.Second), "bob", finishedHelmRead().read)
	cache.mu.Lock()
	_, kept := cache.entries["alice"]
	cache.mu.Unlock()
	if kept {
		t.Fatal("a scope idle past helmIssuesEntryIdle was kept")
	}
}

func TestHelmIssuesCacheCapsEntries(t *testing.T) {
	previous := helmIssuesMaxEntries
	helmIssuesMaxEntries = 3
	t.Cleanup(func() { helmIssuesMaxEntries = previous })
	cache := newTestHelmIssuesCache(t)
	for i := range 6 {
		key := fmt.Sprintf("user-%d", i)
		cache.get(helmWait(t, time.Second), key, finishedHelmRead().read)
		waitIdle(t, cache, key)
	}
	cache.mu.Lock()
	n := len(cache.entries)
	_, newest := cache.entries["user-5"]
	cache.mu.Unlock()
	if n > 3 || !newest {
		t.Fatalf("entries = %d (newest kept: %v), want at most 3 with the newest kept", n, newest)
	}
}

// Reads outlive their requests, so a burst of distinct scopes must not start
// an unbounded number of them; scopes over the cap say they are waiting.
func TestHelmIssuesCacheCapsConcurrentReads(t *testing.T) {
	previousReads, previousEntries := helmIssuesMaxReads, helmIssuesMaxEntries
	helmIssuesMaxReads, helmIssuesMaxEntries = 2, 4
	t.Cleanup(func() { helmIssuesMaxReads, helmIssuesMaxEntries = previousReads, previousEntries })
	cache := newTestHelmIssuesCache(t)
	read := newSlowHelmRead()
	for i := range 10 {
		view := cache.get(helmWait(t, 0), fmt.Sprintf("scope-%d", i), read.read)
		if view.snapshot != nil || view.failure != "" {
			t.Fatalf("scope-%d = %+v, want not checked yet", i, view)
		}
		if i >= 2 && (view.reading || !view.waiting) {
			t.Fatalf("scope-%d over the cap = %+v, want waiting for a slot, not reading", i, view)
		}
	}
	read.waitReads(t, 2)
	time.Sleep(20 * time.Millisecond)
	if got := read.reads.Load(); got != 2 {
		t.Fatalf("reads = %d, want at most 2 running", got)
	}
	cache.mu.Lock()
	n := len(cache.entries)
	cache.mu.Unlock()
	if n > 4 {
		t.Fatalf("entries = %d, want at most 4", n)
	}

	close(read.release)
	for _, key := range []string{"scope-0", "scope-1"} {
		waitIdle(t, cache, key)
	}
	cache.get(helmWait(t, time.Second), "scope-2", read.read)
	read.waitReads(t, 3) // a freed slot is used again
}

func TestHelmIssuesCacheDropsResultOnceAccessIsRevoked(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	cache.get(helmWait(t, time.Second), "alice", finishedHelmRead().read)
	ageEntry(cache, "alice", time.Hour)
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("denied"))
	cache.get(helmWait(t, time.Second), "alice", failingHelmRead(denied))
	waitIdle(t, cache, "alice")
	view := cache.get(helmWait(t, 0), "alice", newSlowHelmRead().read)
	if view.snapshot != nil {
		t.Fatalf("view = %+v, want the earlier result gone after a 403", view)
	}
}

// A result whose refresh can't start because every slot is taken says so,
// instead of passing as current with no read on the way.
func TestHelmIssuesCacheStaleResultReportsWaitingForASlot(t *testing.T) {
	previous := helmIssuesMaxReads
	helmIssuesMaxReads = 1
	t.Cleanup(func() { helmIssuesMaxReads = previous })
	cache := newTestHelmIssuesCache(t)
	cache.get(helmWait(t, time.Second), "alice", finishedHelmRead().read)
	ageEntry(cache, "alice", 2*time.Hour)

	busy := newSlowHelmRead()
	cache.get(helmWait(t, 0), "bob", busy.read)
	busy.waitReads(t, 1)

	view := cache.get(helmWait(t, 0), "alice", newSlowHelmRead().read)
	if view.snapshot == nil || view.reading || !view.waiting {
		t.Fatalf("view = %+v, want the old result, not reading, waiting for a slot", view)
	}
}

// After a failed read the scope waits before rescanning, rather than
// throwing away a full scan on every poll; it still reports the failure.
func TestHelmIssuesCacheBacksOffAfterAFailure(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	now := time.Now()
	cache.now = func() time.Time { return now }
	var attempts atomic.Int32
	failing := func(context.Context) ([]issues.Issue, error) {
		attempts.Add(1)
		return nil, errors.New("apiserver unavailable")
	}
	cache.get(helmWait(t, time.Second), "alice", failing)
	waitIdle(t, cache, "alice")
	for range 3 {
		view := cache.get(helmWait(t, 0), "alice", failing)
		if view.failure != helmReadError || view.failedAt.IsZero() {
			t.Fatalf("view = %+v, want the failure and its time", view)
		}
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d within the backoff, want 1", got)
	}
	now = now.Add(helmIssuesRetryAfterFailure + time.Second)
	cache.get(helmWait(t, time.Second), "alice", failing)
	waitIdle(t, cache, "alice")
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d after the backoff, want 2", got)
	}
}

// After shutdown no new read starts.
func TestHelmIssuesCacheRefusesReadsAfterStop(t *testing.T) {
	cache := newTestHelmIssuesCache(t)
	cache.stop()
	read := newSlowHelmRead()
	view := cache.get(helmWait(t, 50*time.Millisecond), "alice", read.read)
	if view.snapshot != nil || view.reading {
		t.Fatalf("view = %+v after stop", view)
	}
	time.Sleep(20 * time.Millisecond)
	if read.reads.Load() != 0 {
		t.Fatal("a read started after the cache was stopped")
	}
}

// A 403 that lands as the read's time cap runs out is still a denial: the
// earlier result must go.
func TestHelmIssuesCacheForbiddenAtTheDeadlineStillDropsTheResult(t *testing.T) {
	previous := helmIssuesReadTimeout
	helmIssuesReadTimeout = 20 * time.Millisecond
	t.Cleanup(func() { helmIssuesReadTimeout = previous })
	cache := newTestHelmIssuesCache(t)
	cache.get(helmWait(t, time.Second), "alice", finishedHelmRead().read)
	ageEntry(cache, "alice", time.Hour)
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("denied"))
	lateDenial := func(ctx context.Context) ([]issues.Issue, error) {
		<-ctx.Done()
		return nil, denied
	}
	cache.get(helmWait(t, 0), "alice", lateDenial)
	time.Sleep(10 * time.Millisecond)
	waitIdle(t, cache, "alice")
	view := cache.get(helmWait(t, 0), "alice", newSlowHelmRead().read)
	if view.snapshot != nil || view.failure != helmReadForbidden {
		t.Fatalf("view = %+v, want no result and a forbidden failure", view)
	}
}
