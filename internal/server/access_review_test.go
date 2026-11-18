package server

import (
	"context"
	"sync"
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
	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list")
		}()
	}
	time.Sleep(50 * time.Millisecond)
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

// A caller that gives up doesn't fail the others waiting on the same review.
func TestCanReadUserDecision_CanceledLeaderDoesNotFailFollowers(t *testing.T) {
	release := make(chan struct{})
	s, user, _ := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		<-release
		return true
	})
	ctx, cancel := context.WithCancel(context.Background())
	leader := make(chan struct{})
	go func() {
		defer close(leader)
		s.canReadUserDecision(ctx, user, "", "nodes", "", "list")
	}()
	time.Sleep(20 * time.Millisecond)
	follower := make(chan [2]bool, 1)
	go func() {
		allowed, decided := s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list")
		follower <- [2]bool{allowed, decided}
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	close(release)
	<-leader
	if got := <-follower; !got[0] || !got[1] {
		t.Fatalf("follower allowed=%v decided=%v, want allowed", got[0], got[1])
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

// A caller whose permissions entry was replaced while another caller's review
// ran keeps the shared answer, so its next identical check sends no review.
func TestCanReadUserDecision_JoinerKeepsSharedAnswer(t *testing.T) {
	release := make(chan struct{})
	s, user, reviews := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		<-release
		return true
	})
	var wg sync.WaitGroup
	ask := func() {
		defer wg.Done()
		s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list")
	}
	wg.Add(1)
	go ask()
	time.Sleep(50 * time.Millisecond)
	s.permCache.Set(user.Username, user.Groups, &auth.UserPermissions{})
	wg.Add(1)
	go ask()
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if ok, _ := s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list"); !ok {
		t.Fatal("denied after the shared review allowed")
	}
	if len(*reviews) != 1 {
		t.Fatalf("reviews = %d, want the joiner to reuse the shared answer", len(*reviews))
	}
}

// A same-name reconnect can reach a different cluster, so a check after it
// never joins a review sent over the previous connection.
func TestCanReadUserDecision_ReconnectStartsANewReview(t *testing.T) {
	release := make(chan struct{})
	s, user, _ := accessReviewServer(t, func(authv1.ResourceAttributes) bool {
		<-release
		return true
	})
	first := make(chan struct{})
	go func() {
		defer close(first)
		s.canReadUserDecision(context.Background(), user, "", "nodes", "", "list")
	}()
	time.Sleep(50 * time.Millisecond)

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
	var wg sync.WaitGroup
	for _, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.canReadUserDecision(context.Background(), u, "", "nodes", "", "list")
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if len(*reviews) != 2 {
		t.Fatalf("reviews = %d, want one per identity", len(*reviews))
	}
}
