package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// reviewAPI stands in for the apiserver's SubjectAccessReview endpoint,
// answering from decide and counting the reviews it received.
func reviewAPI(t *testing.T, decide func(authorizationv1.ResourceAttributes) bool) (*kubernetes.Clientset, *atomic.Int32) {
	t.Helper()
	var reviews atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			http.NotFound(w, r)
			return
		}
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Spec.ResourceAttributes == nil {
			http.Error(w, "bad review", http.StatusBadRequest)
			return
		}
		reviews.Add(1)
		review.Status.Allowed = decide(*review.Spec.ResourceAttributes)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host:          server.URL,
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
		QPS:           -1,
	})
	if err != nil {
		t.Fatalf("build Kubernetes client: %v", err)
	}
	return client, &reviews
}

// accessReviewUser installs a review API and a cached permissions entry with
// no verdicts for the user, so every check below sends a review.
func accessReviewUser(t *testing.T, decide func(authorizationv1.ResourceAttributes) bool) (context.Context, *atomic.Int32) {
	t.Helper()
	client, reviews := reviewAPI(t, decide)
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	return withTestUserPerms(t, "reader", []string{"viewers"}, nil), reviews
}

// Tool calls asking the same question at once share one review.
func TestCanReadInNamespaceDecision_ConcurrentChecksShareOneReview(t *testing.T) {
	release := make(chan struct{})
	ctx, reviews := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = canReadInNamespaceDecision(ctx, "", "nodes", "", "list")
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
	if n := reviews.Load(); n != 1 {
		t.Fatalf("reviews = %d, want one shared review", n)
	}
}

// A call that gives up doesn't fail the others waiting on the same review.
func TestCanReadInNamespaceDecision_CanceledLeaderDoesNotFailFollowers(t *testing.T) {
	release := make(chan struct{})
	userCtx, _ := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	ctx, cancel := context.WithCancel(userCtx)
	leader := make(chan struct{})
	go func() {
		defer close(leader)
		canReadInNamespaceDecision(ctx, "", "nodes", "", "list")
	}()
	time.Sleep(20 * time.Millisecond)
	follower := make(chan [2]bool, 1)
	go func() {
		allowed, decided := canReadInNamespaceDecision(userCtx, "", "nodes", "", "list")
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

// A call whose context ends stops waiting on a stalled review and fails
// closed without caching.
func TestCanReadInNamespaceDecision_CallerDeadlineEndsTheWait(t *testing.T) {
	release := make(chan struct{})
	userCtx, _ := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	// Registered after the fake apiserver, so it runs first and lets the held
	// review finish before the server closes.
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(userCtx, 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	allowed, decided := canReadInNamespaceDecision(ctx, "", "secrets", "team", "get")
	if allowed || decided {
		t.Fatalf("allowed=%v decided=%v, want a non-authoritative denial", allowed, decided)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %s past the caller's deadline", waited)
	}
	if _, cached := getPermCache().Get("reader", []string{"viewers"}).CanI("get", "", "secrets", "team"); cached {
		t.Fatal("a caller giving up must not cache a verdict")
	}
}

// A call whose permissions entry was replaced while another call's review ran
// keeps the shared answer, so its next identical check sends no review.
func TestCanReadInNamespaceDecision_JoinerKeepsSharedAnswer(t *testing.T) {
	release := make(chan struct{})
	ctx, reviews := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	var wg sync.WaitGroup
	ask := func() {
		defer wg.Done()
		canReadInNamespaceDecision(ctx, "", "nodes", "", "list")
	}
	wg.Add(1)
	go ask()
	time.Sleep(50 * time.Millisecond)
	getPermCache().Set("reader", []string{"viewers"}, &pkgauth.UserPermissions{})
	wg.Add(1)
	go ask()
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if ok, _ := canReadInNamespaceDecision(ctx, "", "nodes", "", "list"); !ok {
		t.Fatal("denied after the shared review allowed")
	}
	if n := reviews.Load(); n != 1 {
		t.Fatalf("reviews = %d, want the joiner to reuse the shared answer", n)
	}
}

// A same-name reconnect can reach a different cluster, so a check after it
// never joins a review sent over the previous connection.
func TestCanReadInNamespaceDecision_ReconnectStartsANewReview(t *testing.T) {
	release := make(chan struct{})
	ctx, _ := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	first := make(chan struct{})
	go func() {
		defer close(first)
		canReadInNamespaceDecision(ctx, "", "nodes", "", "list")
	}()
	time.Sleep(50 * time.Millisecond)

	newClient, newReviews := reviewAPI(t, func(authorizationv1.ResourceAttributes) bool { return false })
	t.Cleanup(k8s.ReconnectTestClient(newClient))
	getPermCache().Invalidate()
	getPermCache().Set("reader", []string{"viewers"}, &pkgauth.UserPermissions{})

	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	allowed, decided := canReadInNamespaceDecision(waitCtx, "", "nodes", "", "list")
	close(release)
	<-first
	if allowed || !decided {
		t.Fatalf("allowed=%v decided=%v, want the new cluster's denial", allowed, decided)
	}
	if n := newReviews.Load(); n != 1 {
		t.Fatalf("reviews on the new connection = %d, want 1", n)
	}
	if v, ok := getPermCache().Get("reader", []string{"viewers"}).CanI("list", "", "nodes", ""); !ok || v {
		t.Fatalf("cached=%v allowed=%v, want the new cluster's denial cached", ok, v)
	}
}

// Identities whose names differ only in where a separator byte falls are
// different subjects to Kubernetes and never share a review.
func TestCanReadInNamespaceDecision_DistinctIdentitiesDoNotShare(t *testing.T) {
	release := make(chan struct{})
	client, reviews := reviewAPI(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	contexts := []context.Context{
		withTestUserPerms(t, "alice", []string{"team\x00prod"}, nil),
		withTestUserPerms(t, "alice\x00team", []string{"prod"}, nil),
	}
	var wg sync.WaitGroup
	for _, ctx := range contexts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			canReadInNamespaceDecision(ctx, "", "nodes", "", "list")
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := reviews.Load(); n != 2 {
		t.Fatalf("reviews = %d, want one per identity", n)
	}
}

// A review that panics fails its callers closed, caches nothing, and leaves
// the process running.
func TestCanReadInNamespaceDecision_PanickingReviewFailsClosed(t *testing.T) {
	client, _ := reviewAPI(t, func(authorizationv1.ResourceAttributes) bool { return true })
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	ctx := withTestUserPerms(t, "reader", nil, nil)
	original := subjectCanI
	subjectCanI = func(context.Context, kubernetes.Interface, string, []string, string, string, string, string) (bool, error) {
		panic("review exploded")
	}
	t.Cleanup(func() { subjectCanI = original })

	allowed, decided := canReadInNamespaceDecision(ctx, "", "nodes", "", "list")
	if allowed || decided {
		t.Fatalf("allowed=%v decided=%v, want a non-authoritative denial", allowed, decided)
	}
	if _, cached := getPermCache().Get("reader", nil).CanI("list", "", "nodes", ""); cached {
		t.Fatal("a panicked review must not cache a verdict")
	}
}
