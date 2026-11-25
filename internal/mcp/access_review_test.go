package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// reviewServer stands in for the apiserver's SubjectAccessReview endpoint. It
// counts the reviews it receives, signals each on arrived before answering
// from decide, and decide may hold the answer.
type reviewServer struct {
	client  *kubernetes.Clientset
	reviews atomic.Int32
	arrived chan struct{}
}

func newReviewServer(t *testing.T, decide func(authorizationv1.ResourceAttributes) bool) *reviewServer {
	t.Helper()
	api := &reviewServer{arrived: make(chan struct{}, 64)}
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
		api.reviews.Add(1)
		api.arrived <- struct{}{}
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
	api.client = client
	return api
}

// awaitReviews blocks until n reviews reached the apiserver.
func (api *reviewServer) awaitReviews(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-api.arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("%d of %d reviews reached the apiserver", i, n)
		}
	}
}

// accessReviewUser installs a review server and a cached permissions entry
// with no verdicts for the user, so every check below sends a review.
func accessReviewUser(t *testing.T, decide func(authorizationv1.ResourceAttributes) bool) (context.Context, *reviewServer) {
	t.Helper()
	api := newReviewServer(t, decide)
	previous := k8s.SetTestClient(api.client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	return withTestUserPerms(t, "reader", []string{"viewers"}, nil), api
}

// Tool calls asking the same question at once share one review.
func TestCanReadInNamespaceDecision_ConcurrentChecksShareOneReview(t *testing.T) {
	release := make(chan struct{})
	ctx, api := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	const n = 8
	baseline := sharedWaiters()
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = canReadInNamespaceDecision(ctx, "", "nodes", "", "list")
		}()
	}
	awaitSharedWaiters(t, baseline, n)
	close(release)
	wg.Wait()
	for i, ok := range results {
		if !ok {
			t.Fatalf("caller %d denied", i)
		}
	}
	if got := api.reviews.Load(); got != 1 {
		t.Fatalf("reviews = %d, want one shared review", got)
	}
}

// A call that gives up while another waits leaves the shared review running
// for the one still waiting.
func TestCanReadInNamespaceDecision_CallGivingUpKeepsTheSharedReview(t *testing.T) {
	release := make(chan struct{})
	userCtx, api := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	waiting := make(chan [2]bool, 1)
	go func() {
		allowed, decided := canReadInNamespaceDecision(userCtx, "", "nodes", "", "list")
		waiting <- [2]bool{allowed, decided}
	}()
	api.awaitReviews(t, 1)
	ctx, cancel := context.WithTimeout(userCtx, 50*time.Millisecond)
	defer cancel()
	if allowed, decided := canReadInNamespaceDecision(ctx, "", "nodes", "", "list"); allowed || decided {
		t.Fatalf("the call that gave up allowed=%v decided=%v, want a non-authoritative denial", allowed, decided)
	}
	close(release)
	if got := <-waiting; !got[0] || !got[1] {
		t.Fatalf("waiting call allowed=%v decided=%v, want allowed", got[0], got[1])
	}
	if got := api.reviews.Load(); got != 1 {
		t.Fatalf("reviews = %d, want the call that gave up to have joined the shared review", got)
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

func TestCanReadInNamespaceDecision_ReviewHasNoShorterDeadlineThanCaller(t *testing.T) {
	userCtx, _ := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool { return true })
	ctx, cancel := context.WithTimeout(userCtx, time.Minute)
	defer cancel()
	callerDeadline, _ := ctx.Deadline()
	original := subjectCanI
	var called bool
	var reviewDeadline time.Time
	subjectCanI = func(ctx context.Context, _ kubernetes.Interface, _ string, _ []string, _, _, _, _ string) (bool, error) {
		called = true
		reviewDeadline, _ = ctx.Deadline()
		return true, nil
	}
	t.Cleanup(func() { subjectCanI = original })

	if allowed, decided := canReadInNamespaceDecision(ctx, "", "secrets", "team", "get"); !allowed || !decided {
		t.Fatalf("allowed=%v decided=%v, want allowed", allowed, decided)
	}
	if !called {
		t.Fatal("the exact review was skipped")
	}
	if !reviewDeadline.IsZero() && reviewDeadline.Before(callerDeadline) {
		t.Fatalf("review deadline %v precedes caller deadline %v", reviewDeadline, callerDeadline)
	}
}

func TestCanReadInNamespaceDecision_FailedDiscoverySharesReview(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host: "https://discovery.invalid",
		QPS:  -1,
		Transport: execRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("discovery unavailable")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	api := discoveryAPI{client: client}
	api.install(t)
	ctx := userContext("reader")
	original := subjectCanI
	t.Cleanup(func() { subjectCanI = original })
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls int
		subjectCanI = func(context.Context, kubernetes.Interface, string, []string, string, string, string, string) (bool, error) {
			calls++
			<-release
			return true, nil
		}
		for range 2 {
			go func() {
				allowed, decided := canReadInNamespaceDecision(ctx, "", "secrets", "team", "get")
				if !allowed || !decided {
					t.Errorf("allowed=%v decided=%v, want the exact review's allow", allowed, decided)
				}
			}()
			synctest.Wait()
		}
		close(release)
		synctest.Wait()
		if calls != 1 || getPermCache().Get("reader", nil) != nil {
			t.Fatalf("reviews = %d, want 1 and no cached permissions", calls)
		}
	})
}

// A call holding the entry that replaced an expired one never joins a review
// started for the old entry: after a revocation it gets the apiserver's new
// denial, and the old review's allow never lands in the new entry.
func TestCanReadInNamespaceDecision_NewEntryNeverJoinsAnOldReview(t *testing.T) {
	release := make(chan struct{})
	var revoked, held atomic.Bool
	ctx, api := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool {
		allowed := !revoked.Load()
		if held.CompareAndSwap(false, true) {
			<-release
		}
		return allowed
	})
	before := make(chan bool, 1)
	go func() {
		allowed, _ := canReadInNamespaceDecision(ctx, "", "secrets", "team", "get")
		before <- allowed
	}()
	api.awaitReviews(t, 1)

	revoked.Store(true)
	getPermCache().Invalidate()
	getPermCache().Set("reader", []string{"viewers"}, &pkgauth.UserPermissions{})
	after := make(chan [2]bool, 1)
	go func() {
		allowed, decided := canReadInNamespaceDecision(ctx, "", "secrets", "team", "get")
		after <- [2]bool{allowed, decided}
	}()
	select {
	case got := <-after:
		if got[0] || !got[1] {
			t.Errorf("after the revocation allowed=%v decided=%v, want the new denial", got[0], got[1])
		}
	case <-time.After(2 * time.Second):
		t.Error("the new entry's call joined the review started for the expired entry")
	}
	close(release)
	if !<-before {
		t.Fatal("the old entry's call lost its own review's allow")
	}
	if v, ok := getPermCache().Get("reader", []string{"viewers"}).CanI("get", "", "secrets", "team"); !ok || v {
		t.Fatalf("new entry cached=%v allowed=%v, want the new denial", ok, v)
	}
	if got := api.reviews.Load(); got != 2 {
		t.Fatalf("reviews = %d, want one per entry", got)
	}
}

// A same-name reconnect can reach a different cluster, so a check after it
// never joins a review sent over the previous connection.
func TestCanReadInNamespaceDecision_ReconnectStartsANewReview(t *testing.T) {
	release := make(chan struct{})
	ctx, api := accessReviewUser(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	first := make(chan struct{})
	go func() {
		defer close(first)
		canReadInNamespaceDecision(ctx, "", "nodes", "", "list")
	}()
	api.awaitReviews(t, 1)

	fresh := newReviewServer(t, func(authorizationv1.ResourceAttributes) bool { return false })
	t.Cleanup(k8s.ReconnectTestClient(fresh.client))
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
	if got := fresh.reviews.Load(); got != 1 {
		t.Fatalf("reviews on the new connection = %d, want 1", got)
	}
	if v, ok := getPermCache().Get("reader", []string{"viewers"}).CanI("list", "", "nodes", ""); !ok || v {
		t.Fatalf("cached=%v allowed=%v, want the new cluster's denial cached", ok, v)
	}
}

// Identities whose names differ only in where a separator byte falls are
// different subjects to Kubernetes and never share a review: both reviews
// reach the apiserver while each is held.
func TestCanReadInNamespaceDecision_DistinctIdentitiesDoNotShare(t *testing.T) {
	release := make(chan struct{})
	api := newReviewServer(t, func(authorizationv1.ResourceAttributes) bool {
		<-release
		return true
	})
	previous := k8s.SetTestClient(api.client)
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
	api.awaitReviews(t, 2)
	close(release)
	wg.Wait()
}

// A review that panics fails its callers closed, caches nothing, and leaves
// the process running.
func TestCanReadInNamespaceDecision_PanickingReviewFailsClosed(t *testing.T) {
	client := newReviewServer(t, func(authorizationv1.ResourceAttributes) bool { return true }).client
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
