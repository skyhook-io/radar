package server

import (
	"encoding/json"
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
	decision, scoped := s.computeSearchKindRBAC(r, []string{"a", "b"}, "", "allowed")
	if decision != "" || scoped != nil || checks.Load() != 1 {
		t.Fatalf("cluster-first: %s %v; checks %d", decision, scoped, checks.Load())
	}
	decision, scoped = s.computeSearchKindRBAC(r, []string{"a", "b"}, "", "secrets")
	if decision != "permission_check_failed" || len(scoped) != 1 || scoped[0] != "a" || checks.Load() != 4 {
		t.Fatalf("partial permission failure: %s %v; checks %d", decision, scoped, checks.Load())
	}
	decision, scoped = s.computeSearchKindRBAC(r, []string{"a"}, "", "broken")
	if decision != "permission_check_failed" || len(scoped) != 0 || checks.Load() != 5 {
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

// A namespace an ns: term names but the search leaves out is attributed to the
// caller's RBAC (namespace_excluded) or to the namespace pick
// (namespace_scope_limited); a pick that only narrows an unnamed scope is
// never reported.
func TestSearchAttributesNamedNamespacesToRBACOrSelection(t *testing.T) {
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
		{"search-erin", "q=robusta", 2, nil},
		{"search-erin", "q=robusta&globalNs=1", 4, nil},
		{"search-erin", "q=kind:Pod+robusta", 1, nil},
		// A namespace that doesn't exist is reported as not found to an
		// unrestricted caller, never as the pick's doing.
		{"search-erin", "q=ns:team-b+ns:missing+robusta", 0, []unsearchedEntry{
			{Kind: "*", Reason: "namespace_not_found", Namespaces: []string{"missing"}},
			{Kind: "*", Reason: "outside_namespace_selection", Namespaces: []string{"team-b"}},
		}},
		{"search-erin", "q=ns:team-b+ns:missing+robusta&globalNs=1", 2, []unsearchedEntry{{Kind: "*", Reason: "namespace_not_found", Namespaces: []string{"missing"}}}},
		{"search-frank", "q=ns:team-b+ns:other+robusta", 0, []unsearchedEntry{
			{Kind: "*", Reason: "outside_namespace_selection", Namespaces: []string{"team-b"}},
			{Kind: "*", Reason: "user_namespace_not_visible", Namespaces: []string{"other"}},
		}},
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
