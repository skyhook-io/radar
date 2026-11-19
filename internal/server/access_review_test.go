package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func accessReviewServer(t *testing.T, decide func(authv1.ResourceAttributes) bool) (*Server, *auth.User, *[]authv1.ResourceAttributes) {
	t.Helper()
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "reader", Groups: []string{"viewers"}}
	s.permCache.Set(user.Username, user.Groups, &auth.UserPermissions{})
	return s, user, fakeSARServer(t, decide)
}

// Callers asking the same question at once share one review.
func TestCanReadUserDecision_ConcurrentChecksShareOneReview(t *testing.T) {
	release := make(chan struct{})
	s, user, reviews := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		<-release
		return true
	})
	baseline := sharedWaiters()
	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list")
		}()
	}
	awaitSharedWaiters(t, baseline, len(results))
	close(release)
	wg.Wait()
	for i, ok := range results {
		if !ok {
			t.Fatalf("caller %d denied", i)
		}
	}
	if len(*reviews) != 1 {
		t.Fatalf("reviews = %d, want one shared review", len(*reviews))
	}
}

// A caller that gives up doesn't fail the others waiting on the same review,
// even the caller that started it.
func TestCanReadUserDecision_CanceledLeaderDoesNotFailFollowers(t *testing.T) {
	release := make(chan struct{})
	s, user, reviews := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		<-release
		return true
	})
	baseline := sharedWaiters()
	ctx, cancel := context.WithCancel(context.Background())
	leader := make(chan struct{})
	go func() {
		defer close(leader)
		s.canReadUserDecision(ctx, user, "", "nodes", "", "list")
	}()
	awaitSharedWaiters(t, baseline, 1)
	follower := make(chan [2]bool, 1)
	go func() {
		allowed, decided := s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list")
		follower <- [2]bool{allowed, decided}
	}()
	awaitSharedWaiters(t, baseline, 2)
	cancel()
	<-leader
	close(release)
	if got := <-follower; !got[0] || !got[1] {
		t.Fatalf("follower allowed=%v decided=%v, want allowed", got[0], got[1])
	}
	if len(*reviews) != 1 {
		t.Fatalf("reviews = %d, want the follower to have kept the leader's review", len(*reviews))
	}
}

// A caller whose request ends stops waiting on a stalled review and fails
// closed without caching.
func TestCanReadUserDecision_CallerDeadlineEndsTheWait(t *testing.T) {
	release := make(chan struct{})
	s, user, _ := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		<-release
		return true
	})
	// Registered after the fake apiserver, so it runs first and lets the held
	// review finish before the server closes.
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	allowed, decided := s.canReadUserDecision(ctx, user, "", "secrets", "team", "get")
	if allowed || decided {
		t.Fatalf("allowed=%v decided=%v, want a non-authoritative denial", allowed, decided)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %s past the caller's deadline", waited)
	}
	if _, cached := s.permCache.Get(user.Username, user.Groups).CanI("get", "", "secrets", "team"); cached {
		t.Fatal("a caller giving up must not cache a verdict")
	}
}

// A caller holding the entry that replaced an expired one never joins a review
// started for the old entry: after a revocation it gets the apiserver's new
// denial, and the old review's allow never lands in the new entry.
func TestCanReadUserDecision_NewEntryNeverJoinsAnOldReview(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	var revoked, held atomic.Bool
	s, user, reviews := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		allowed := !revoked.Load()
		if held.CompareAndSwap(false, true) {
			arrived <- struct{}{}
			<-release
		}
		return allowed
	})
	before := make(chan bool, 1)
	go func() {
		allowed, _ := s.canReadUserDecision(context.Background(), user, "", "secrets", "team", "get")
		before <- allowed
	}()
	awaitArrival(t, arrived)

	revoked.Store(true)
	s.permCache.Invalidate()
	s.permCache.Set(user.Username, user.Groups, &auth.UserPermissions{})
	after := make(chan [2]bool, 1)
	go func() {
		allowed, decided := s.canReadUserDecision(context.Background(), user, "", "secrets", "team", "get")
		after <- [2]bool{allowed, decided}
	}()
	select {
	case got := <-after:
		if got[0] || !got[1] {
			t.Errorf("after the revocation allowed=%v decided=%v, want the new denial", got[0], got[1])
		}
	case <-time.After(2 * time.Second):
		t.Error("the new entry's caller joined the review started for the expired entry")
	}
	close(release)
	if !<-before {
		t.Fatal("the old entry's caller lost its own review's allow")
	}
	if v, ok := s.permCache.Get(user.Username, user.Groups).CanI("get", "", "secrets", "team"); !ok || v {
		t.Fatalf("new entry cached=%v allowed=%v, want the new denial", ok, v)
	}
	if len(*reviews) != 2 {
		t.Fatalf("reviews = %d, want one per entry", len(*reviews))
	}
}

// A caller that gives up while another waits leaves the shared review running
// for the one still waiting.
func TestCanReadUserDecision_CallerGivingUpKeepsTheSharedReview(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	s, user, reviews := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		arrived <- struct{}{}
		<-release
		return true
	})
	waiting := make(chan bool, 1)
	go func() {
		allowed, _ := s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list")
		waiting <- allowed
	}()
	awaitArrival(t, arrived)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if allowed, decided := s.canReadUserDecision(ctx, user, "", "nodes", "", "list"); allowed || decided {
		t.Fatalf("the caller that gave up allowed=%v decided=%v, want a non-authoritative denial", allowed, decided)
	}
	close(release)
	if !<-waiting {
		t.Fatal("the waiting caller was denied when another gave up")
	}
	if len(*reviews) != 1 {
		t.Fatalf("reviews = %d, want the caller that gave up to have joined the shared review", len(*reviews))
	}
}

// A shared review stops at accessReviewTimeout and fails closed, even for a
// caller with no deadline of its own.
func TestCanReadUserDecision_SharedReviewKeepsItsCap(t *testing.T) {
	previous := accessReviewTimeout
	accessReviewTimeout = 50 * time.Millisecond
	t.Cleanup(func() { accessReviewTimeout = previous })
	release := make(chan struct{})
	s, user, _ := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		<-release
		return true
	})
	// Registered after the fake apiserver, so it runs first and lets the held
	// review finish before the server closes.
	t.Cleanup(func() { close(release) })
	start := time.Now()
	allowed, decided := s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list")
	if allowed || decided {
		t.Fatalf("allowed=%v decided=%v, want a non-authoritative denial at the cap", allowed, decided)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("waited %s past the cap", waited)
	}
	if _, cached := s.permCache.Get(user.Username, user.Groups).CanI("list", "", "nodes", ""); cached {
		t.Fatal("a review stopped at the cap must not cache a verdict")
	}
}

func awaitArrival(t *testing.T, arrived <-chan struct{}) {
	t.Helper()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the review never reached the apiserver")
	}
}

// A same-name reconnect can reach a different cluster, so a check after it
// never joins a review sent over the previous connection.
func TestCanReadUserDecision_ReconnectStartsANewReview(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	s, user, _ := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		arrived <- struct{}{}
		<-release
		return true
	})
	first := make(chan struct{})
	go func() {
		defer close(first)
		s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list")
	}()
	awaitArrival(t, arrived)

	newClient, newReviews := fakeSARClient(t, func(authv1.ResourceAttributes) bool { return false })
	t.Cleanup(k8s.ReconnectTestClient(newClient))
	s.permCache.Invalidate()
	s.permCache.Set(user.Username, user.Groups, &auth.UserPermissions{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	allowed, decided := s.canReadUserDecision(ctx, user, "", "nodes", "", "list")
	close(release)
	<-first
	if allowed || !decided {
		t.Fatalf("allowed=%v decided=%v, want the new cluster's denial", allowed, decided)
	}
	if len(*newReviews) != 1 {
		t.Fatalf("reviews on the new connection = %d, want 1", len(*newReviews))
	}
	if v, ok := s.permCache.Get(user.Username, user.Groups).CanI("list", "", "nodes", ""); !ok || v {
		t.Fatalf("cached=%v allowed=%v, want the new cluster's denial cached", ok, v)
	}
}

// Identities whose names differ only in where a separator byte falls are
// different subjects to Kubernetes and never share a review.
func TestCanReadUserDecision_DistinctIdentitiesDoNotShare(t *testing.T) {
	release := make(chan struct{})
	s, _, reviews := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		<-release
		return true
	})
	users := []*auth.User{
		{Username: "alice", Groups: []string{"team\x00prod"}},
		{Username: "alice\x00team", Groups: []string{"prod"}},
	}
	baseline := sharedWaiters()
	var wg sync.WaitGroup
	for _, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.canReadUserDecision(context.Background(), u, "", "nodes", "", "list")
		}()
	}
	awaitSharedWaiters(t, baseline, len(users))
	close(release)
	wg.Wait()
	if len(*reviews) != 2 {
		t.Fatalf("reviews = %d, want one per identity", len(*reviews))
	}
}
