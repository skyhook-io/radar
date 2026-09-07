package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// fakeSARServer stands in for the apiserver's SubjectAccessReview endpoint,
// answering from decide and recording every review's resource attributes.
func fakeSARServer(t *testing.T, decide func(authv1.ResourceAttributes) bool) *[]authv1.ResourceAttributes {
	t.Helper()
	reviews := &[]authv1.ResourceAttributes{}
	var mu sync.Mutex
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			http.NotFound(w, r)
			return
		}
		var review authv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Spec.ResourceAttributes == nil {
			t.Errorf("decode SubjectAccessReview: %v", err)
			http.Error(w, "bad review", http.StatusBadRequest)
			return
		}
		attrs := *review.Spec.ResourceAttributes
		mu.Lock()
		*reviews = append(*reviews, attrs)
		mu.Unlock()
		review.Status.Allowed = decide(attrs)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(review); err != nil {
			t.Errorf("encode SubjectAccessReview: %v", err)
		}
	}))
	t.Cleanup(apiServer.Close)

	// client-go defaults to protobuf; the fake decodes JSON.
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host:          apiServer.URL,
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
	})
	if err != nil {
		t.Fatalf("build Kubernetes client: %v", err)
	}
	previousClient := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previousClient) })
	return reviews
}

func TestPrometheusAuthGate_NoAuthPassesThrough(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "none"})
	reviews := fakeSARServer(t, func(authv1.ResourceAttributes) bool { return false })

	r := httptest.NewRequest(http.MethodGet, "/api/prometheus/cluster", nil)
	if !s.prometheusAuthGate(r, "", "pods", "", "list") {
		t.Fatal("no-auth request should pass the cluster-wide gate")
	}
	if !s.prometheusAuthGate(r, "apps", "deployments", "beta", "get") {
		t.Fatal("no-auth request should pass the per-resource gate")
	}
	if len(*reviews) != 0 {
		t.Fatalf("no-auth path must not issue a SubjectAccessReview, got %+v", *reviews)
	}
}

// A user whose RBAC covers namespace alpha only. The apiserver grants
// everything inside alpha and refuses the all-namespaces list, which is
// what a namespaced Role produces.
func TestPrometheusAuthGate_NamespaceScopedUser(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}})
	reviews := fakeSARServer(t, func(a authv1.ResourceAttributes) bool { return a.Namespace == "alpha" })
	r := requestWithUser(http.MethodGet, "/api/prometheus/cluster", &auth.User{Username: "alice"})

	if s.prometheusAuthGate(r, "", "pods", "", "list") {
		t.Error("namespace-scoped user must fail the cluster-wide gate")
	}
	if len(*reviews) != 1 {
		t.Fatalf("expected one SubjectAccessReview, got %+v", *reviews)
	}
	if got := (*reviews)[0]; got.Namespace != "" || got.Resource != "pods" || got.Verb != "list" || got.Group != "" {
		t.Errorf("cluster-wide gate reviewed %+v, want an all-namespaces list pods check", got)
	}

	if !s.prometheusAuthGate(r, "apps", "deployments", "alpha", "get") {
		t.Error("user should read metrics for a Deployment in their own namespace")
	}
	if !s.prometheusAuthGate(r, "autoscaling", "horizontalpodautoscalers", "alpha", "get") {
		t.Error("user should read metrics for an HPA in their own namespace")
	}
	if s.prometheusAuthGate(r, "apps", "deployments", "beta", "get") {
		t.Error("user must not read metrics for a Deployment outside their namespace")
	}
	if s.prometheusAuthGate(r, "", "nodes", "", "get") {
		t.Error("user must not read Node metrics without the cluster-scoped nodes grant")
	}
}

// A SAR that says yes is not enough on its own: the namespace must also be
// in the user's discovered allow-list, matching the main resource API.
func TestPrometheusAuthGate_SARAllowedButNamespaceFilteredOut(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}})
	fakeSARServer(t, func(authv1.ResourceAttributes) bool { return true })
	r := requestWithUser(http.MethodGet, "/api/prometheus/resources/Deployment/beta/web", &auth.User{Username: "alice"})

	if s.prometheusAuthGate(r, "apps", "deployments", "beta", "get") {
		t.Error("a namespace outside the user's allow-list must be refused even when the SAR allows the read")
	}
	if !s.prometheusAuthGate(r, "apps", "deployments", "alpha", "get") {
		t.Error("a namespace inside the allow-list with an allowing SAR should pass")
	}
}

func TestPrometheusAuthGate_ClusterWideUser(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("bob", nil, &auth.UserPermissions{AllowedNamespaces: nil})
	reviews := fakeSARServer(t, func(a authv1.ResourceAttributes) bool {
		return a.Namespace == "" && a.Resource == "pods" && a.Verb == "list"
	})
	r := requestWithUser(http.MethodGet, "/api/prometheus/query", &auth.User{Username: "bob"})

	if !s.prometheusAuthGate(r, "", "pods", "", "list") {
		t.Fatal("cluster-wide reader should pass the cluster-wide gate")
	}
	if !s.prometheusAuthGate(r, "", "pods", "", "list") {
		t.Fatal("second call should pass from the memoized verdict")
	}
	if len(*reviews) != 1 {
		t.Errorf("SubjectAccessReview issued %d times, want 1 (memoized on the user's permissions)", len(*reviews))
	}
}

// A SubjectAccessReview the apiserver cannot answer denies the read and is
// not memoized, so a transient failure does not lock the user out for the
// cache TTL.
func TestPrometheusAuthGate_SARErrorFailsClosedWithoutCaching(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("bob", nil, &auth.UserPermissions{AllowedNamespaces: nil})
	var failing atomic.Bool
	failing.Store(true)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			http.Error(w, "apiserver unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authv1.SubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}})
	}))
	t.Cleanup(apiServer.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: apiServer.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatalf("build Kubernetes client: %v", err)
	}
	previousClient := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previousClient) })
	r := requestWithUser(http.MethodGet, "/api/prometheus/query", &auth.User{Username: "bob"})

	if s.prometheusAuthGate(r, "", "pods", "", "list") {
		t.Fatal("a failing SubjectAccessReview must deny")
	}
	failing.Store(false)
	if !s.prometheusAuthGate(r, "", "pods", "", "list") {
		t.Fatal("the failed verdict must not be cached: an answering apiserver should now allow")
	}
}
