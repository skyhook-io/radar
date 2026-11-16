package server

import (
	"context"
	"sync"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"

	"github.com/skyhook-io/radar/internal/auth"
)

func accessReviewServer(t *testing.T, decide func(authv1.ResourceAttributes) bool) (*Server, *auth.User, *[]authv1.ResourceAttributes) {
	t.Helper()
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "reader", Groups: []string{"viewers"}}
	s.permCache.Set(user.Username, user.Groups, &auth.UserPermissions{ExpiresAt: time.Now().Add(time.Minute)})
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
// closed without caching, as it did before reviews were shared.
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
