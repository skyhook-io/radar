package server

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"k8s.io/client-go/kubernetes/fake"
)

// Pods and ConfigMaps in alpha, Services in alpha and beta, Deployments and Nodes everywhere, Secrets not collected.
var (
	collectionScopes = map[string]k8score.ResourceScope{
		k8score.Pods:        {Enabled: true, Namespace: "alpha"},
		k8score.ConfigMaps:  {Enabled: true, Namespace: "alpha"},
		k8score.Services:    {Enabled: true, Namespace: "alpha"},
		k8score.Deployments: {Enabled: true},
		k8score.Nodes:       {Enabled: true},
		k8score.Secrets:     {Namespace: "alpha"},
	}
	collectionNamespaces = map[string][]string{k8score.Services: {"alpha", "beta"}}
)

func newCollectionTestCache(t *testing.T, scopes map[string]k8score.ResourceScope, namespaces map[string][]string) *k8s.ResourceCache {
	t.Helper()
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:                  fake.NewClientset(),
		ResourceScopes:          scopes,
		ResourceScopeNamespaces: namespaces,
		DeferredTypes:           map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	return &k8s.ResourceCache{ResourceCache: core}
}

func describeCollection(c *NamespaceCollection) []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.Groups))
	for _, g := range c.Groups {
		kinds := make([]string, 0, len(g.Kinds))
		for _, kind := range g.Kinds {
			kinds = append(kinds, kind.Kind)
		}
		out = append(out, strings.Join(kinds, ",")+": "+strings.Join(g.Namespaces.Names, ","))
	}
	return out
}

func TestNamespaceCollectionGroupsKindsByNamespaces(t *testing.T) {
	cache := newCollectionTestCache(t, collectionScopes, collectionNamespaces)

	got := namespaceCollectionFor(cache, nil, []string{"alpha", "beta", "gamma"})
	want := []string{"ConfigMap,Pod: alpha", "Service: alpha,beta"}
	if described := describeCollection(got); !slices.Equal(described, want) {
		t.Fatalf("collection = %v, want %v (cluster-wide, cluster-only and uncollected kinds are left out)", described, want)
	}
	if got.MayBeIncomplete {
		t.Error("collection reads as incomplete without a truncated candidate list")
	}
	if n := got.Groups[1].Namespaces.Count; n != 2 {
		t.Errorf("Service group counts %d namespaces, want 2", n)
	}
	if kinds := got.Groups[0].Kinds; kinds[1].Kind != "Pod" || kinds[1].Group != "" {
		t.Errorf("Pod is %+v, want the core group", kinds[1])
	}
}

func TestNamespaceCollectionNamesOnlyVisibleNamespaces(t *testing.T) {
	cache := newCollectionTestCache(t, collectionScopes, collectionNamespaces)

	got := namespaceCollectionFor(cache, nil, []string{"beta"})
	if want := []string{"ConfigMap,Pod: ", "Service: beta"}; !slices.Equal(describeCollection(got), want) {
		t.Fatalf("collection = %v, want %v", describeCollection(got), want)
	}
	if got.Groups[0].Namespaces.Count != 0 {
		t.Errorf("Pod group counts %d namespaces, want none: a hidden namespace is not counted", got.Groups[0].Namespaces.Count)
	}
	if body, _ := json.Marshal(got); strings.Contains(string(body), "alpha") {
		t.Errorf("a namespace the caller can't see is in %s", body)
	}

	// Kinds that differ only in namespaces the caller can't see form one group.
	got = namespaceCollectionFor(cache, nil, nil)
	if want := []string{"ConfigMap,Pod,Service: "}; !slices.Equal(describeCollection(got), want) {
		t.Fatalf("collection with nothing visible = %v, want %v", describeCollection(got), want)
	}
}

func TestNamespaceCollectionIsAbsentWhenNothingIsNarrowed(t *testing.T) {
	cache := newCollectionTestCache(t, map[string]k8score.ResourceScope{
		k8score.Pods:        {Enabled: true},
		k8score.Deployments: {Enabled: true},
		k8score.Nodes:       {Enabled: true},
	}, nil)

	if got := namespaceCollectionFor(cache, &k8s.PermissionCheckResult{ScopeCandidatesTruncated: true}, []string{"alpha"}); got != nil {
		t.Fatalf("collection = %v, want none: nothing is collected in only some namespaces", describeCollection(got))
	}
	if got := namespaceCollectionFor(nil, nil, []string{"alpha"}); got != nil {
		t.Fatalf("collection without a cache = %v, want none", describeCollection(got))
	}
}

func TestNamespaceCollectionMayBeIncompleteWhenCandidatesWereTruncated(t *testing.T) {
	cache := newCollectionTestCache(t, collectionScopes, collectionNamespaces)

	for _, tc := range []struct {
		permissions *k8s.PermissionCheckResult
		want        bool
	}{
		{nil, false},
		{&k8s.PermissionCheckResult{}, false},
		{&k8s.PermissionCheckResult{ScopeCandidatesTruncated: true}, true},
	} {
		if got := namespaceCollectionFor(cache, tc.permissions, []string{"alpha"}); got.MayBeIncomplete != tc.want {
			t.Errorf("permissions %+v: MayBeIncomplete = %v, want %v", tc.permissions, got.MayBeIncomplete, tc.want)
		}
	}
}

// Pods in "default"; Services in "default" and "broken", the fixture cluster's namespaces.
var (
	handlerCollectionScopes = map[string]k8score.ResourceScope{
		k8score.Pods:        {Enabled: true, Namespace: "default"},
		k8score.Services:    {Enabled: true, Namespace: "default"},
		k8score.Deployments: {Enabled: true},
	}
	handlerCollectionNamespaces = map[string][]string{k8score.Services: {"default", "broken"}}
)

func useScopedTestResourceCache(t *testing.T, scopes map[string]k8score.ResourceScope, namespaces map[string][]string) {
	t.Helper()
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCacheNamespaces(fake.NewClientset(), scopes, namespaces); err != nil {
		t.Fatalf("initialize scoped test resource cache: %v", err)
	}
}

func TestNamespaceScopeReportsWhereRadarCollects(t *testing.T) {
	fakeNamespaceAPIServer(t, http.StatusOK, http.StatusForbidden, "default", "broken")
	useScopedTestResourceCache(t, handlerCollectionScopes, handlerCollectionNamespaces)

	var scope NamespaceScopeResponse
	assertOK(t, get(t, "/api/cluster/namespace-scope"), &scope)
	if want := []string{"Pod: default", "Service: broken,default"}; !slices.Equal(describeCollection(scope.Collection), want) {
		t.Fatalf("collection = %v, want %v", describeCollection(scope.Collection), want)
	}
	if scope.Collection.MayBeIncomplete {
		t.Error("collection reads as incomplete when Radar could list every namespace")
	}
	if len(scope.Actives) != 0 {
		t.Errorf("the collection narrowed the user's own filter: actives = %v", scope.Actives)
	}
}

func TestNamespaceScopeOmitsCollectionWhenEverythingIsCollectedClusterWide(t *testing.T) {
	fakeNamespaceAPIServer(t, http.StatusOK, http.StatusForbidden, "default", "broken")

	var raw map[string]json.RawMessage
	assertOK(t, get(t, "/api/cluster/namespace-scope"), &raw)
	if body, ok := raw["collection"]; ok {
		t.Fatalf("collection = %s, want it omitted for a cluster-wide collector", body)
	}
}

func TestNamespaceScopeOmitsCollectionUnderNamespacePin(t *testing.T) {
	fakeNamespaceAPIServer(t, http.StatusOK, http.StatusForbidden, "default", "broken")
	useScopedTestResourceCache(t, handlerCollectionScopes, handlerCollectionNamespaces)
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("default")
	t.Cleanup(func() { k8s.ForceNamespaceScope = false; k8s.SetFallbackNamespace("") })

	var scope NamespaceScopeResponse
	assertOK(t, get(t, "/api/cluster/namespace-scope"), &scope)
	if scope.Collection != nil {
		t.Fatalf("collection = %v under the pin, want none: the picker already says Radar watches one namespace", describeCollection(scope.Collection))
	}
}

// An empty visible list after a failed load must not read as "none of them".
func TestNamespaceScopeOmitsCollectionWhenTheNamespaceListFailed(t *testing.T) {
	fakeNamespaceAPIServer(t, http.StatusInternalServerError, http.StatusForbidden)
	useScopedTestResourceCache(t, handlerCollectionScopes, handlerCollectionNamespaces)

	var scope NamespaceScopeResponse
	assertOK(t, get(t, "/api/cluster/namespace-scope"), &scope)
	if scope.Collection != nil {
		t.Fatalf("collection = %v after a failed namespace load, want none", describeCollection(scope.Collection))
	}
}

func TestNamespaceScopeCollectionMayBeIncompleteWithoutNamespaceListAccess(t *testing.T) {
	prevNs := k8s.SetTestContextNamespace("default")
	t.Cleanup(func() { k8s.SetTestContextNamespace(prevNs) })
	fakeNamespaceAPIServer(t, http.StatusForbidden, http.StatusForbidden)
	useScopedTestResourceCache(t, handlerCollectionScopes, handlerCollectionNamespaces)
	t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{
		Perms:                    &k8s.ResourcePermissions{},
		Scopes:                   handlerCollectionScopes,
		ScopeNamespaces:          handlerCollectionNamespaces,
		ScopeCandidatesTruncated: true,
	}))

	var scope NamespaceScopeResponse
	assertOK(t, get(t, "/api/cluster/namespace-scope"), &scope)
	if want := []string{"Pod,Service: default"}; !slices.Equal(describeCollection(scope.Collection), want) {
		t.Fatalf("collection = %v, want %v", describeCollection(scope.Collection), want)
	}
	if !scope.Collection.MayBeIncomplete {
		t.Error("collection does not say it may be incomplete")
	}
}

// One collector answers every user; each learns only the namespaces they can see.
func TestNamespaceScopeCollectionFollowsTheCallersNamespaces(t *testing.T) {
	prev := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prev) })
	fakeNamespaceAPIServer(t, http.StatusOK, http.StatusForbidden, "default", "broken")
	useScopedTestResourceCache(t, handlerCollectionScopes, handlerCollectionNamespaces)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	env.srv.permCache.Set("carol", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default", "broken"}})

	resp := env.authGet(t, "/api/cluster/namespace-scope", "alice", "")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("alice: status %d, read error %v", resp.StatusCode, err)
	}
	var alice NamespaceScopeResponse
	if err := json.Unmarshal(body, &alice); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Pod,Service: default"}; !slices.Equal(describeCollection(alice.Collection), want) {
		t.Errorf("alice's collection = %v, want %v", describeCollection(alice.Collection), want)
	}
	if strings.Contains(string(body), "broken") {
		t.Errorf("a namespace alice can't see is in her response: %s", body)
	}

	var carol NamespaceScopeResponse
	assertOK(t, env.authGet(t, "/api/cluster/namespace-scope", "carol", ""), &carol)
	if want := []string{"Pod: default", "Service: broken,default"}; !slices.Equal(describeCollection(carol.Collection), want) {
		t.Errorf("carol's collection = %v, want %v", describeCollection(carol.Collection), want)
	}
}

// Every kind the counts report as narrowed by collection is in the summary, in the same namespaces.
func TestNamespaceCollectionAgreesWithResourceCounts(t *testing.T) {
	fakeNamespaceAPIServer(t, http.StatusOK, http.StatusForbidden, "default", "broken")
	useScopedTestResourceCache(t, handlerCollectionScopes, handlerCollectionNamespaces)

	var counts ResourceCountsResponse
	assertOK(t, get(t, "/api/resource-counts"), &counts)
	var scope NamespaceScopeResponse
	assertOK(t, get(t, "/api/cluster/namespace-scope"), &scope)

	summarized := map[string][]string{}
	if scope.Collection != nil {
		for _, group := range scope.Collection.Groups {
			for _, kind := range group.Kinds {
				key := kind.Kind
				if kind.Group != "" {
					key = kind.Group + "/" + kind.Kind
				}
				summarized[key] = group.Namespaces.Names
			}
		}
	}
	narrowed := map[string][]string{}
	for key, coverage := range counts.Coverage {
		if slices.Contains(coverage.NarrowedBy, resourceapi.NarrowedByNamespaceCollection) {
			narrowed[key] = coverage.Scope.Namespaces.Names
		}
	}
	if len(narrowed) == 0 {
		t.Fatalf("counts report no kind narrowed by collection: %+v", counts.Coverage)
	}
	if len(summarized) != len(narrowed) {
		t.Fatalf("summary names %v, counts narrow %v", summarized, narrowed)
	}
	for key, namespaces := range narrowed {
		if !slices.Equal(summarized[key], namespaces) {
			t.Errorf("%s: summary has %v, counts have %v", key, summarized[key], namespaces)
		}
	}
}
