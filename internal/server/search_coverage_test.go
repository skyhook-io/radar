package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
)

func TestSearchNewNamespacedKindsReportCallerDenial(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"}},
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "limits", Namespace: "default"}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "quota", Namespace: "default"}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "default"}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "binding", Namespace: "default"}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"}},
	))
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	env.srv.permCache.Set("search-kind-reader", nil, perms)
	for _, kind := range search.NamespacedSearchKinds {
		t.Run(kind.Kind, func(t *testing.T) {
			perms.SetCanI("list", kind.Group, kind.Resource, "", false)
			perms.SetCanI("list", kind.Group, kind.Resource, "default", false)
			resp := env.authGet(t, "/api/search?context=none&q=kind:"+kind.Kind, "search-kind-reader", "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			var result search.Result
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, gap := range result.Unsearched {
				if gap.Kind == kind.Kind && gap.Group == kind.Group && gap.Reason == "rbac_denied" {
					found = true
				}
			}
			if !result.Partial || !found || len(result.Hits) != 0 {
				t.Fatalf("kind gate: %+v", result)
			}
			perms.SetCanI("list", kind.Group, kind.Resource, "default", true)
			allowed := env.authGet(t, "/api/search?context=none&q=kind:"+kind.Kind, "search-kind-reader", "")
			defer allowed.Body.Close()
			if allowed.StatusCode != http.StatusOK {
				t.Fatalf("allowed status %d", allowed.StatusCode)
			}
			if err := json.NewDecoder(allowed.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if len(result.Hits) != 1 {
				t.Fatalf("authorized kind omitted: %+v", result)
			}
			for _, gap := range result.Unsearched {
				if gap.Kind == kind.Kind {
					t.Fatalf("authorized kind reports gap: %+v", result)
				}
			}
		})
	}
}

func TestSearchOrdinaryNamespacedKindsUseNamespaceVisibility(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"}},
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "limits", Namespace: "default"}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "quota", Namespace: "default"}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"}},
	))
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	env.srv.permCache.Set("search-ordinary", nil, perms)
	for _, tc := range []struct{ kind, group, resource string }{
		{"ServiceAccount", "", "serviceaccounts"}, {"LimitRange", "", "limitranges"},
		{"ResourceQuota", "", "resourcequotas"}, {"NetworkPolicy", "networking.k8s.io", "networkpolicies"},
	} {
		perms.SetCanI("list", tc.group, tc.resource, "", false)
		perms.SetCanI("list", tc.group, tc.resource, "default", false)
		resp := env.authGet(t, "/api/search?context=none&q=kind:"+tc.kind, "search-ordinary", "")
		var result search.Result
		err := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil || len(result.Hits) != 1 {
			t.Fatalf("%s namespace-visible kind omitted: %+v, %v", tc.kind, result, err)
		}
		for _, gap := range result.Unsearched {
			if gap.Kind == tc.kind {
				t.Fatalf("ordinary kind gated: %+v", gap)
			}
		}
	}
}

func TestSearchKindRBACClusterFirstAndFailureReason(t *testing.T) {
	var checks atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks.Add(1)
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
			http.Error(w, "decode", 400)
			return
		}
		attrs := review.Spec.ResourceAttributes
		if attrs.Resource == "broken" || attrs.Namespace == "b" {
			http.Error(w, "SAR unavailable", 503)
			return
		}
		review.Status.Allowed = attrs.Resource == "allowed" || attrs.Namespace == "a"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "search-checks"}
	s.permCache.Set(user.Username, nil, &auth.UserPermissions{AllowedNamespaces: []string{"a", "b"}})
	r := requestWithUser("GET", "/api/search", user)
	decision, scoped := s.computeSearchKindRBAC(r, []string{"a", "b"}, "", "allowed")
	if decision != "" || scoped != nil || checks.Load() != 1 {
		t.Fatalf("cluster-first: %s %v; checks %d", decision, scoped, checks.Load())
	}
	decision, scoped = s.computeSearchKindRBAC(r, []string{"a", "b"}, "", "secrets")
	if decision != "list_error" || len(scoped) != 1 || scoped[0] != "a" || checks.Load() != 4 {
		t.Fatalf("partial permission failure: %s %v; checks %d", decision, scoped, checks.Load())
	}
	decision, scoped = s.computeSearchKindRBAC(r, []string{"a"}, "", "broken")
	if decision != "list_error" || len(scoped) != 0 || checks.Load() != 5 {
		t.Fatalf("cluster check failure: %s %v; checks %d", decision, scoped, checks.Load())
	}
}

func TestSearchKindRBACDeniedClusterCountsAndCachesNamespaceChecks(t *testing.T) {
	var checks atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks.Add(1)
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
			http.Error(w, "decode", 400)
			return
		}
		review.Status.Allowed = review.Spec.ResourceAttributes.Namespace != "" && review.Spec.ResourceAttributes.Namespace != "denied"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "search-denied-counts"}
	namespaces := []string{"a", "b", "c", "denied"}
	s.permCache.Set(user.Username, nil, &auth.UserPermissions{AllowedNamespaces: namespaces})
	r := requestWithUser("GET", "/api/search", user)
	for range 2 {
		for _, kind := range search.NamespacedSearchKinds {
			decision, scoped := s.computeSearchKindRBAC(r, namespaces, kind.Group, kind.Resource)
			if decision != "override" || len(scoped) != 3 {
				t.Fatalf("%s denied-cluster scope: %s %v", kind.Kind, decision, scoped)
			}
		}
		if got, want := checks.Load(), int32(len(search.NamespacedSearchKinds)*(len(namespaces)+1)); got != want {
			t.Fatalf("SAR calls = %d, want %d (one cluster check plus visible namespaces per sensitive kind, cached on repeat)", got, want)
		}
	}
}
