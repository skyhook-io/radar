package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// waitForReviewers blocks until n callers wait on shared reviews.
func waitForReviewers(t *testing.T, a *AccessReviews, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		waiting := 0
		for _, f := range a.flights {
			waiting += f.waiters
		}
		a.mu.Unlock()
		if waiting == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d callers waiting, want %d", waiting, n)
		}
		time.Sleep(time.Millisecond)
	}
}

var reviewer = &User{Username: "reader", Groups: []string{"viewers"}}

func decide(a *AccessReviews, ctx context.Context, perms *UserPermissions, review func(context.Context) (bool, bool)) (bool, bool) {
	return a.Decide(ctx, reviewer, perms, "1", AccessReviewQuestion{Verb: "list", Resource: "nodes"}, review)
}

// heldReview answers allowed once release closes, counting its runs.
func heldReview(release <-chan struct{}, calls *atomic.Int32) func(context.Context) (bool, bool) {
	return func(ctx context.Context) (bool, bool) {
		calls.Add(1)
		select {
		case <-release:
			return true, true
		case <-ctx.Done():
			return false, false
		}
	}
}

// Callers asking the same question of the same entry at once share one review,
// and its verdict is recorded once in that entry.
func TestAccessReviews_ConcurrentChecksShareOneReview(t *testing.T) {
	var a AccessReviews
	perms := &UserPermissions{}
	release := make(chan struct{})
	var calls atomic.Int32
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if allowed, decided := decide(&a, context.Background(), perms, heldReview(release, &calls)); !allowed || !decided {
				t.Errorf("caller %d: allowed=%v decided=%v", i, allowed, decided)
			}
		}()
	}
	waitForReviewers(t, &a, n)
	close(release)
	wg.Wait()
	if c := calls.Load(); c != 1 {
		t.Fatalf("reviews = %d, want 1", c)
	}
	if v, ok := perms.CanI("list", "", "nodes", ""); !ok || !v {
		t.Fatal("the shared verdict was not recorded in the entry")
	}
}

func TestAccessReviews_ServesACachedVerdict(t *testing.T) {
	var a AccessReviews
	perms := &UserPermissions{}
	perms.SetCanI("list", "", "nodes", "", false)
	var called bool
	allowed, decided := decide(&a, context.Background(), perms, func(context.Context) (bool, bool) {
		called = true
		return true, true
	})
	if called || allowed || !decided {
		t.Fatalf("allowed=%v decided=%v, want the cached denial", allowed, decided)
	}
}

// The caller that started a review giving up doesn't fail the others waiting
// on it, and they still share that one review.
func TestAccessReviews_StarterLeavingDoesNotFailOthers(t *testing.T) {
	var a AccessReviews
	perms := &UserPermissions{}
	release := make(chan struct{})
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	starter := make(chan [2]bool, 1)
	go func() {
		allowed, decided := decide(&a, ctx, perms, heldReview(release, &calls))
		starter <- [2]bool{allowed, decided}
	}()
	waitForReviewers(t, &a, 1)
	other := make(chan [2]bool, 1)
	go func() {
		allowed, decided := decide(&a, context.Background(), perms, heldReview(release, &calls))
		other <- [2]bool{allowed, decided}
	}()
	waitForReviewers(t, &a, 2)
	cancel()
	if got := <-starter; got[0] || got[1] {
		t.Fatalf("starter allowed=%v decided=%v, want a non-authoritative denial", got[0], got[1])
	}
	close(release)
	if got := <-other; !got[0] || !got[1] {
		t.Fatalf("other caller allowed=%v decided=%v, want allowed", got[0], got[1])
	}
	if c := calls.Load(); c != 1 {
		t.Fatalf("reviews = %d, want 1", c)
	}
}

// A review ends when no caller waits for it, as a caller's own review did.
func TestAccessReviews_LastCallerLeavingCancelsTheReview(t *testing.T) {
	var a AccessReviews
	perms := &UserPermissions{}
	canceled := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	allowed, decided := decide(&a, ctx, perms, func(runCtx context.Context) (bool, bool) {
		<-runCtx.Done()
		close(canceled)
		return false, false
	})
	if allowed || decided {
		t.Fatalf("allowed=%v decided=%v, want a non-authoritative denial", allowed, decided)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the review kept going after its only caller left")
	}
	if _, ok := perms.CanI("list", "", "nodes", ""); ok {
		t.Fatal("a review nobody waited for recorded a verdict")
	}
}

// A review all its callers left, finishing after a successor recorded a newer
// verdict in the same entry, never overwrites that verdict.
func TestAccessReviews_AbandonedReviewNeverOverwritesItsSuccessor(t *testing.T) {
	var a AccessReviews
	perms := &UserPermissions{}
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	abandoned := make(chan [2]bool, 1)
	go func() {
		// The apiserver has allowed; only the review's completion is held.
		allowed, decided := decide(&a, ctx, perms, func(context.Context) (bool, bool) {
			<-release
			return true, true
		})
		abandoned <- [2]bool{allowed, decided}
	}()
	waitForReviewers(t, &a, 1)
	a.mu.Lock()
	var old *flight[reviewVerdict]
	for _, f := range a.flights {
		old = f
	}
	a.mu.Unlock()
	cancel()
	if got := <-abandoned; got[0] || got[1] {
		t.Fatalf("the caller that left allowed=%v decided=%v, want a non-authoritative denial", got[0], got[1])
	}

	allowed, decided := decide(&a, context.Background(), perms, func(context.Context) (bool, bool) {
		return false, true
	})
	if allowed || !decided {
		t.Fatalf("the successor allowed=%v decided=%v, want its denial", allowed, decided)
	}
	close(release)
	<-old.done
	if v, ok := perms.CanI("list", "", "nodes", ""); !ok || v {
		t.Fatalf("cached=%v allowed=%v, want the successor's denial kept", ok, v)
	}
}

// A shared review has no deadline of its own: a slow verdict reaches a caller
// whose own context allows the wait.
func TestAccessReviews_ReviewKeepsTheCallersBudget(t *testing.T) {
	var a AccessReviews
	var hasDeadline bool
	allowed, decided := decide(&a, context.Background(), &UserPermissions{}, func(ctx context.Context) (bool, bool) {
		_, hasDeadline = ctx.Deadline()
		return true, true
	})
	if hasDeadline {
		t.Fatal("the shared review carries a deadline its callers didn't set")
	}
	if !allowed || !decided {
		t.Fatalf("allowed=%v decided=%v, want allowed", allowed, decided)
	}
}

// A caller holding the entry that replaced an expired one never joins a review
// started for the old entry, and the old review's verdict lands only in the
// old entry.
func TestAccessReviews_NewEntryNeverJoinsAnOldReview(t *testing.T) {
	var a AccessReviews
	old := &UserPermissions{}
	release := make(chan struct{})
	var calls atomic.Int32
	before := make(chan [2]bool, 1)
	go func() {
		allowed, decided := decide(&a, context.Background(), old, heldReview(release, &calls))
		before <- [2]bool{allowed, decided}
	}()
	waitForReviewers(t, &a, 1)

	fresh := &UserPermissions{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	allowed, decided := decide(&a, ctx, fresh, func(context.Context) (bool, bool) {
		calls.Add(1)
		return false, true
	})
	close(release)
	if got := <-before; !got[0] || !got[1] {
		t.Fatalf("the old entry's caller allowed=%v decided=%v, want its own review's allow", got[0], got[1])
	}
	if allowed || !decided {
		t.Fatalf("the new entry's caller allowed=%v decided=%v, want its own review's denial", allowed, decided)
	}
	if v, ok := fresh.CanI("list", "", "nodes", ""); !ok || v {
		t.Fatalf("new entry cached=%v allowed=%v, want its own denial", ok, v)
	}
	if v, ok := old.CanI("list", "", "nodes", ""); !ok || !v {
		t.Fatalf("old entry cached=%v allowed=%v, want its own allow", ok, v)
	}
	if c := calls.Load(); c != 2 {
		t.Fatalf("reviews = %d, want one per entry", c)
	}
}

// Only an apiserver verdict is recorded; a failed or panicking review fails
// closed and the next caller asks again.
func TestAccessReviews_FailuresFailClosedAndAreNotRecorded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		review func(context.Context) (bool, bool)
	}{
		{"non-authoritative allow", func(context.Context) (bool, bool) { return true, false }},
		{"unreachable", func(context.Context) (bool, bool) { return false, false }},
		{"panic", func(context.Context) (bool, bool) { panic("review exploded") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a AccessReviews
			perms := &UserPermissions{}
			if allowed, decided := decide(&a, context.Background(), perms, tc.review); allowed || decided {
				t.Fatalf("allowed=%v decided=%v, want a non-authoritative denial", allowed, decided)
			}
			if _, ok := perms.CanI("list", "", "nodes", ""); ok {
				t.Fatal("a failed review recorded a verdict")
			}
			var calls atomic.Int32
			decide(&a, context.Background(), perms, func(context.Context) (bool, bool) {
				calls.Add(1)
				return true, true
			})
			if c := calls.Load(); c != 1 {
				t.Fatal("the caller after a failure didn't review again")
			}
		})
	}
}

// Callers with nothing cached share a review only with each other, and record
// nothing.
func TestAccessReviews_CallersWithoutAnEntryShareAndRecordNothing(t *testing.T) {
	var a AccessReviews
	release := make(chan struct{})
	var calls atomic.Int32
	const n = 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decide(&a, context.Background(), nil, heldReview(release, &calls))
		}()
	}
	waitForReviewers(t, &a, n)
	entry := &UserPermissions{}
	entryDone := make(chan struct{})
	go func() {
		defer close(entryDone)
		decide(&a, context.Background(), entry, heldReview(release, &calls))
	}()
	waitForReviewers(t, &a, n+1)
	close(release)
	wg.Wait()
	<-entryDone
	if c := calls.Load(); c != 2 {
		t.Fatalf("reviews = %d, want one for the callers without an entry and one for the entry", c)
	}
	decide(&a, context.Background(), nil, heldReview(release, &calls))
	if c := calls.Load(); c != 3 {
		t.Fatal("a review without an entry left a verdict behind")
	}
}

// Callers on different connections, or with identities that differ only in
// where a separator byte falls, never share a review.
func TestAccessReviews_DistinctConnectionsAndIdentitiesDoNotShare(t *testing.T) {
	type caller struct {
		user       *User
		connection string
	}
	for _, tc := range []struct {
		name string
		a, b caller
	}{
		{"connection", caller{reviewer, "1"}, caller{reviewer, "2"}},
		{"identity", caller{&User{Username: "alice", Groups: []string{"team\x00prod"}}, "1"}, caller{&User{Username: "alice\x00team", Groups: []string{"prod"}}, "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a AccessReviews
			release := make(chan struct{})
			var calls atomic.Int32
			var wg sync.WaitGroup
			for i, c := range []caller{tc.a, tc.b} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					a.Decide(context.Background(), c.user, nil, c.connection, AccessReviewQuestion{Verb: "list", Resource: "nodes"}, heldReview(release, &calls))
				}()
				waitForReviewers(t, &a, i+1)
			}
			close(release)
			wg.Wait()
			if c := calls.Load(); c != 2 {
				t.Fatalf("reviews = %d, want one each", c)
			}
		})
	}
}

func TestAccessReviews_AlreadyCanceledCallerFailsClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var a AccessReviews
		perms := &UserPermissions{}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		for _, warm := range []bool{false, true} {
			if warm {
				perms.SetCanI("list", "", "nodes", "", true)
			}
			var calls atomic.Int32
			allowed, decided := decide(&a, ctx, perms, func(context.Context) (bool, bool) {
				calls.Add(1)
				return true, true
			})
			synctest.Wait()
			if allowed || decided || calls.Load() != 0 {
				t.Fatalf("warm=%v: canceled caller got %v, %v or started review", warm, allowed, decided)
			}
		}
	})
}

func TestFlight_CanceledCallerWithReadyResult(t *testing.T) {
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan struct{})
	close(done)
	for range 100 {
		f := flight[bool]{done: done, waiters: 1, result: true}
		if got, ok := f.await(ctx, &mu, nil); got || ok || f.waiters != 0 {
			t.Fatalf("canceled caller got %v, %v; waiters = %d", got, ok, f.waiters)
		}
	}
}
