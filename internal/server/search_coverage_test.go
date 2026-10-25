package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
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
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

var expectedExactSearchKinds = []struct{ Kind, Group, Resource string }{
	{"Secret", "", "secrets"},
	{"Role", "rbac.authorization.k8s.io", "roles"},
	{"RoleBinding", "rbac.authorization.k8s.io", "rolebindings"},
	{"LimitRange", "", "limitranges"},
}

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
	for _, kind := range expectedExactSearchKinds {
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
			for _, gap := range unsearched(result) {
				if gap.Kind == kind.Kind && gap.Group == kind.Group && gap.Reason == "user_forbidden" {
					found = true
				}
			}
			if !partial(result) || !found || len(result.Hits) != 0 {
				t.Fatalf("kind gate: %+v", result)
			}
			perms.SetCanI("list", kind.Group, kind.Resource, "default", true)
			allowed := env.authGet(t, "/api/search?context=none&q=kind:"+kind.Kind, "search-kind-reader", "")
			defer allowed.Body.Close()
			if allowed.StatusCode != http.StatusOK {
				t.Fatalf("allowed status %d", allowed.StatusCode)
			}
			var allowedResult search.Result
			if err := json.NewDecoder(allowed.Body).Decode(&allowedResult); err != nil {
				t.Fatal(err)
			}
			if len(allowedResult.Hits) != 1 {
				t.Fatalf("authorized kind omitted: %+v", allowedResult)
			}
			for _, gap := range unsearched(allowedResult) {
				if gap.Kind == kind.Kind {
					t.Fatalf("authorized kind reports gap: %+v", unsearched(allowedResult))
				}
			}
		})
	}
}

func TestSearchOrdinaryNamespacedKindsUseNamespaceVisibility(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "quota", Namespace: "default"}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"}},
	))
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	env.srv.permCache.Set("search-ordinary", nil, perms)
	for _, tc := range []struct{ kind, group, resource string }{
		{"ServiceAccount", "", "serviceaccounts"},
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
		for _, gap := range unsearched(result) {
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
			http.Error(w, "SAR unavailable", http.StatusServiceUnavailable)
			return
		}
		review.Status.Allowed = attrs.Resource == "allowed" || attrs.Namespace == "a"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
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
	gate := s.computeSearchKindRBAC(r)
	allowed, denied, unchecked := gate([]string{"a", "b"}, "", "allowed")
	if !slices.Equal(allowed, []string{"a", "b"}) || len(denied) != 0 || len(unchecked) != 0 || checks.Load() != 1 {
		t.Fatalf("cluster-first: %v %v %v; checks %d", allowed, denied, unchecked, checks.Load())
	}
	allowed, denied, unchecked = gate([]string{"a", "b"}, "", "secrets")
	if !slices.Equal(allowed, []string{"a"}) || len(denied) != 0 || !slices.Equal(unchecked, []string{"b"}) || checks.Load() != 4 {
		t.Fatalf("partial permission failure: %v %v %v; checks %d", allowed, denied, unchecked, checks.Load())
	}
	allowed, denied, unchecked = gate([]string{"a"}, "", "broken")
	if len(allowed) != 0 || len(denied) != 0 || !slices.Equal(unchecked, []string{"a"}) || checks.Load() != 5 {
		t.Fatalf("cluster check failure: %v %v %v; checks %d", allowed, denied, unchecked, checks.Load())
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
		_ = json.NewEncoder(w).Encode(review)
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
		gate := s.computeSearchKindRBAC(r)
		for _, kind := range expectedExactSearchKinds {
			allowed, denied, unchecked := gate(namespaces, kind.Group, kind.Resource)
			if !slices.Equal(allowed, []string{"a", "b", "c"}) || !slices.Equal(denied, []string{"denied"}) || len(unchecked) != 0 {
				t.Fatalf("%s denied-cluster scope: %v %v %v", kind.Kind, allowed, denied, unchecked)
			}
		}
		if got, want := checks.Load(), int32(len(expectedExactSearchKinds)*(len(namespaces)+1)); got != want {
			t.Fatalf("SAR calls = %d, want %d (one cluster check plus visible namespaces per sensitive kind, cached on repeat)", got, want)
		}
	}
}

func searchAs(t *testing.T, env *authTestEnv, user, query string) search.Result {
	t.Helper()
	resp := env.authGet(t, "/api/search?context=none&include=none&"+query, user, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: status %d", query, resp.StatusCode)
	}
	var result search.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func hitKeys(result search.Result) []string {
	var keys []string
	for _, hit := range result.Hits {
		keys = append(keys, hit.Kind+"/"+hit.Namespace+"/"+hit.Name)
	}
	slices.Sort(keys)
	return keys
}

func namespaceGaps(result search.Result) []unsearchedEntry {
	var gaps []unsearchedEntry
	for _, gap := range unsearched(result) {
		if gap.Kind == "*" && gap.Namespaces != nil {
			gaps = append(gaps, gap)
		}
	}
	return gaps
}

func TestSearchClusterScopedHitsForNamespaceRestrictedCaller(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "robusta-pod", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "robusta-hidden", Namespace: "team-b"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "robusta-cr"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "robusta-control-plane"}},
	))
	env := newAuthTestServer(t)
	grant := func(perms *auth.UserPermissions, allowed bool) *auth.UserPermissions {
		perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterroles", "", allowed)
		perms.SetCanI("list", "", "nodes", "", allowed)
		return perms
	}
	env.srv.permCache.Set("search-carol", nil, grant(&auth.UserPermissions{AllowedNamespaces: []string{"default"}}, true))
	env.srv.permCache.Set("search-dave", nil, grant(&auth.UserPermissions{AllowedNamespaces: []string{"default"}}, false))

	for _, tc := range []struct{ query, want string }{
		{"q=kind:ClusterRole+robusta", "ClusterRole//robusta-cr"},
		{"q=kind:Node+control-plane", "Node//robusta-control-plane"},
		{"q=kind:ClusterRole+ns:team-b+robusta", "ClusterRole//robusta-cr"},
	} {
		result := searchAs(t, env, "search-carol", tc.query)
		if got := hitKeys(result); !slices.Equal(got, []string{tc.want}) {
			t.Fatalf("%s: hits %v, want [%s]; coverage %+v", tc.query, got, tc.want, unsearched(result))
		}
	}
	result := searchAs(t, env, "search-carol", "q=robusta")
	if got, want := hitKeys(result), []string{"ClusterRole//robusta-cr", "Node//robusta-control-plane", "Pod/default/robusta-pod"}; !slices.Equal(got, want) {
		t.Fatalf("broad hits %v, want %v", got, want)
	}
	if gaps := namespaceGaps(result); len(gaps) != 0 {
		t.Fatalf("RBAC namespace ceiling reported as a gap: %+v", gaps)
	}
	result = searchAs(t, env, "search-carol", "q=ns:default+robusta")
	if got, want := hitKeys(result), []string{"Pod/default/robusta-pod"}; !slices.Equal(got, want) {
		t.Fatalf("broad ns: query hits %v, want %v", got, want)
	}

	for _, query := range []string{"q=kind:ClusterRole+robusta", "q=kind:Node+control-plane"} {
		result := searchAs(t, env, "search-dave", query)
		denied := false
		for _, gap := range unsearched(result) {
			denied = denied || gap.Reason == "user_forbidden"
		}
		if len(result.Hits) != 0 || !denied {
			t.Fatalf("%s without cluster-scoped grant: %+v", query, result)
		}
	}
}

// A user's saved namespace pick never narrows a search. A namespace an ns:
// term names but the search leaves out is the caller's RBAC
// (user_namespace_not_visible), or namespace_not_found for a caller who can
// see every namespace.
func TestSearchIgnoresTheSavedPickAndAttributesNamedNamespacesToRBAC(t *testing.T) {
	prev := k8s.SetTestContextName("search-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prev) })
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "robusta-pod", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "robusta-other", Namespace: "team-b"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "robusta-config", Namespace: "team-b"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "robusta-cr"}},
	))
	env := newAuthTestServer(t)
	unrestricted := &auth.UserPermissions{}
	unrestricted.SetCanI("list", "rbac.authorization.k8s.io", "clusterroles", "", true)
	env.srv.permCache.Set("search-erin", nil, unrestricted)
	env.srv.setActiveNamespaceForUser(requestWithUser("GET", "/", &auth.User{Username: "search-erin"}), []string{"default"})
	restricted := &auth.UserPermissions{AllowedNamespaces: []string{"default", "team-b"}}
	env.srv.permCache.Set("search-frank", nil, restricted)
	env.srv.setActiveNamespaceForUser(requestWithUser("GET", "/", &auth.User{Username: "search-frank"}), []string{"default"})

	for _, tc := range []struct {
		user, query string
		hits        int
		want        []unsearchedEntry
	}{
		// A saved pick never narrows a search: with or without globalNs=1 it
		// covers the caller's whole access.
		{"search-erin", "q=robusta", 4, nil},
		{"search-erin", "q=robusta&globalNs=1", 4, nil},
		{"search-erin", "q=kind:Pod+robusta", 2, nil},
		// A namespace that doesn't exist is reported as not found to an
		// unrestricted caller.
		{"search-erin", "q=ns:team-b+ns:missing+robusta", 2, []unsearchedEntry{{Kind: "*", Reason: "namespace_not_found", Namespaces: []string{"missing"}}}},
		{"search-erin", "q=ns:team-b+ns:missing+robusta&globalNs=1", 2, []unsearchedEntry{{Kind: "*", Reason: "namespace_not_found", Namespaces: []string{"missing"}}}},
		{"search-frank", "q=ns:team-b+ns:other+robusta", 2, []unsearchedEntry{{Kind: "*", Reason: "user_namespace_not_visible", Namespaces: []string{"other"}}}},
		{"search-frank", "q=ns:team-b+ns:other+robusta&globalNs=1", 2, []unsearchedEntry{{Kind: "*", Reason: "user_namespace_not_visible", Namespaces: []string{"other"}}}},
	} {
		result := searchAs(t, env, tc.user, tc.query)
		if got := namespaceGaps(result); !reflect.DeepEqual(got, tc.want) || len(result.Hits) != tc.hits {
			t.Fatalf("%s %s: namespace coverage %+v hits %v, want %+v and %d hits", tc.user, tc.query, got, hitKeys(result), tc.want, tc.hits)
		}
	}
}

// A namespace discovery that fails is fail-closed, and the search says so as a
// failed permission check: never a complete empty result, never "you can't see
// that namespace".
func TestSearchReportsFailedNamespaceDiscovery(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "robusta-pod", Namespace: "default"}},
	))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "SAR unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	env := newAuthTestServer(t)

	for _, tc := range []struct {
		query string
		want  unsearchedEntry
	}{
		{"q=kind:Pod+robusta", unsearchedEntry{Kind: "Pod", Reason: "user_permission_check_failed"}},
		{"q=ns:default+robusta", unsearchedEntry{Kind: "*", Reason: "user_permission_check_failed"}},
	} {
		result := searchAs(t, env, "search-unknown", tc.query)
		if !partial(result) || len(result.Hits) != 0 || !slices.ContainsFunc(unsearched(result), func(gap unsearchedEntry) bool { return reflect.DeepEqual(gap, tc.want) }) || len(namespaceGaps(result)) != 0 {
			t.Fatalf("%s: %+v", tc.query, result)
		}
	}
}

// The --namespace-scope pin is Radar's setup, reported for a namespace an ns:
// term names whether or not the search drops the saved pick.
func TestSearchAttributesNamedNamespacesToThePin(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "robusta-pod", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "robusta-other", Namespace: "team-b"}},
	))
	env := newAuthTestServer(t)
	env.srv.permCache.Set("search-pinned", nil, &auth.UserPermissions{})
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("default")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})
	want := []unsearchedEntry{{Kind: "*", Reason: "outside_namespace_pin", Namespaces: []string{"team-b"}}}
	for _, query := range []string{"q=ns:team-b+robusta", "q=ns:team-b+robusta&globalNs=1", "q=kind:Pod+ns:team-b+robusta&globalNs=1"} {
		result := searchAs(t, env, "search-pinned", query)
		if got := namespaceGaps(result); !reflect.DeepEqual(got, want) || len(result.Hits) != 0 {
			t.Fatalf("%s: namespace coverage %+v hits %v, want %+v", query, got, hitKeys(result), want)
		}
	}
	// A namespace this cluster doesn't have isn't the pin's doing.
	want = []unsearchedEntry{{Kind: "*", Reason: "namespace_not_found", Namespaces: []string{"payments-prd"}}}
	for _, query := range []string{"q=ns:payments-prd+robusta", "q=ns:payments-prd+robusta&globalNs=1"} {
		if got := namespaceGaps(searchAs(t, env, "search-pinned", query)); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: namespace coverage %+v, want %+v", query, got, want)
		}
	}
}

func TestSearchKindRBACBudgetAndCancellation(t *testing.T) {
	var checks atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks.Add(1)
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
			http.Error(w, "decode", 400)
			return
		}
		review.Status.Allowed = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "search-budget"}
	perms := &auth.UserPermissions{}
	s.permCache.Set(user.Username, nil, perms)
	namespaces := make([]string, 70)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("ns-%03d", i)
	}
	namespaces = append(namespaces, "cached")
	slices.Sort(namespaces)
	for _, kind := range expectedExactSearchKinds {
		if kind.Kind != "LimitRange" {
			perms.SetCanI("list", kind.Group, kind.Resource, "", false)
		}
		perms.SetCanI("list", kind.Group, kind.Resource, "cached", true)
	}
	r := requestWithUser("GET", "/api/search", user)
	gate := s.computeSearchKindRBAC(r)
	totalAllowed, totalUnchecked := 0, 0
	for _, kind := range expectedExactSearchKinds {
		allowed, denied, unchecked := gate(namespaces, kind.Group, kind.Resource)
		if len(denied) != 0 || !slices.Contains(allowed, "cached") || len(allowed)+len(unchecked) != len(namespaces) {
			t.Fatalf("%s: %v %v %v", kind.Kind, allowed, denied, unchecked)
		}
		totalAllowed += len(allowed)
		totalUnchecked += len(unchecked)
	}
	if checks.Load() != readscope.MaxAccessChecks || totalAllowed != readscope.MaxAccessChecks+len(expectedExactSearchKinds) || totalUnchecked != 70*len(expectedExactSearchKinds)-readscope.MaxAccessChecks {
		t.Fatalf("checks=%d allowed=%d unchecked=%d", checks.Load(), totalAllowed, totalUnchecked)
	}
	perms.SetCanI("list", "aquasecurity.github.io", "vulnerabilityreports", "", false)
	perms.SetCanI("list", "aquasecurity.github.io", "vulnerabilityreports", "cached", true)
	trivy := s.searchKindAccess(r, gate)
	if allowed, authoritative := trivy.ClusterWide("aquasecurity.github.io", "vulnerabilityreports"); allowed || !authoritative {
		t.Fatal("denied Trivy cluster grant")
	}
	trivyAllowed, trivyUnchecked := trivy.InNamespaces("aquasecurity.github.io", "vulnerabilityreports", namespaces)
	if !slices.Equal(trivyAllowed, []string{"cached"}) || len(trivyUnchecked) != 70 || checks.Load() != readscope.MaxAccessChecks {
		t.Fatalf("budget not shared with Trivy: %v %v checks=%d", trivyAllowed, trivyUnchecked, checks.Load())
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	allowed, denied, unchecked := s.computeSearchKindRBAC(r.WithContext(ctx))(namespaces, "", "uncached")
	if len(allowed) != 0 || len(denied) != 0 || !slices.Equal(unchecked, namespaces) || checks.Load() != readscope.MaxAccessChecks {
		t.Fatalf("cancelled: %v %v %v checks=%d", allowed, denied, unchecked, checks.Load())
	}
}

func TestSearchKindRBACMixedPartitionsReachCoverage(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "db-creds"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "beta", Name: "db-denied"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "gamma", Name: "db-unchecked"}},
	))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
			http.Error(w, "decode", 400)
			return
		}
		ns := review.Spec.ResourceAttributes.Namespace
		if ns == "gamma" {
			http.Error(w, "SAR unavailable", http.StatusServiceUnavailable)
			return
		}
		review.Status.Allowed = ns == "alpha"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	env := newAuthTestServer(t)
	user := &auth.User{Username: "search-mixed"}
	env.srv.permCache.Set(user.Username, nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta", "gamma"}})
	r := requestWithUser("GET", "/api/search", user)
	allowed, denied, unchecked := env.srv.computeSearchKindRBAC(r)([]string{"alpha", "beta", "gamma"}, "", "secrets")
	if !slices.Equal(allowed, []string{"alpha"}) || !slices.Equal(denied, []string{"beta"}) || !slices.Equal(unchecked, []string{"gamma"}) {
		t.Fatalf("partitions: %v %v %v", allowed, denied, unchecked)
	}
	body := searchAs(t, env, user.Username, "q=kind:Secret+db")
	if !slices.Equal(hitKeys(body), []string{"Secret/alpha/db-creds"}) {
		t.Fatalf("hits: %+v", body.Hits)
	}
	for _, code := range []readreason.Code{readreason.UserForbidden, readreason.UserPermissionCheckFailed} {
		if !slices.ContainsFunc(body.Coverage.Gaps, func(gap resourceapi.Gap) bool {
			return gap.Code == code && gap.Kinds != nil && slices.Contains(gap.Kinds.Names, resourceapi.KindRef{Kind: "Secret"}) && reflect.DeepEqual(gap.Namespaces, resourceapi.UnknownNames())
		}) {
			t.Fatalf("missing %s partial gap: %+v", code, body.Coverage)
		}
	}
}

func TestSearchTrivyClusterCheckKeepsAuthority(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	r := requestWithUser("GET", "/api/search", &auth.User{Username: "search-trivy-authority"})
	for _, tc := range []struct {
		name                   string
		denied, unchecked      []string
		allowed, authoritative bool
	}{
		{"allowed", nil, nil, true, true},
		{"denied", []string{""}, nil, false, true},
		{"unchecked", nil, []string{""}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := func(ns []string, group, resource string) ([]string, []string, []string) {
				if ns != nil || group != "aquasecurity.github.io" || resource != "vulnerabilityreports" {
					t.Fatalf("wrong gate: %v %s/%s", ns, group, resource)
				}
				return nil, tc.denied, tc.unchecked
			}
			access := s.searchKindAccess(r, gate)
			allowed, authoritative := access.ClusterWide("aquasecurity.github.io", "vulnerabilityreports")
			if allowed != tc.allowed || authoritative != tc.authoritative {
				t.Fatalf("got %v/%v, want %v/%v", allowed, authoritative, tc.allowed, tc.authoritative)
			}
		})
	}
}
