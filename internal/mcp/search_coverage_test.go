package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
)

func TestSearchNamespacedKindRBAC(t *testing.T) {
	for _, kind := range search.NamespacedSearchKinds {
		t.Run(kind.Kind, func(t *testing.T) {
			username := "search-kind-" + kind.Kind
			ctx := withClusterAdmin(t, username)
			perms := getPermCache().Get(username, nil)
			perms.SetCanI("list", kind.Group, kind.Resource, "", false)
			perms.SetCanI("list", kind.Group, kind.Resource, "a", true)
			perms.SetCanI("list", kind.Group, kind.Resource, "b", false)
			decision, ns := mcpSearchKindRBAC(ctx, nil, kind.Group, kind.Resource)
			if decision != "skip" {
				t.Fatalf("cluster-scope bypass: %s %v", decision, ns)
			}
			decision, ns = mcpSearchKindRBAC(ctx, []string{"a", "b"}, kind.Group, kind.Resource)
			if decision != "override" || len(ns) != 1 || ns[0] != "a" {
				t.Fatalf("kind override: %s %v", decision, ns)
			}
			decision, _ = mcpSearchKindRBAC(ctx, []string{"b"}, kind.Group, kind.Resource)
			if decision != "skip" {
				t.Fatalf("denied namespace bypass: %s", decision)
			}
			decision, _ = mcpSearchKindRBAC(context.Background(), nil, kind.Group, kind.Resource)
			if decision != "" {
				t.Fatalf("no-auth gate: %s", decision)
			}
		})
	}
	t.Cleanup(func() { getPermCache().Invalidate() })
}

func TestHandleSearchExcludedNamespaceCoverage(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "excluded-search", []string{"alpha"})
	result, _, err := handleSearch(ctx, nil, searchInput{Query: "kind:Pod", Namespace: "beta", Context: "none"})
	if err != nil {
		t.Fatal(err)
	}
	var body search.Result
	if err = json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Partial || len(body.Hits) != 0 {
		t.Fatalf("scope exclusion: %+v", body)
	}
	found := false
	for _, gap := range body.Unsearched {
		if gap.Kind == "Pod" && gap.Reason == "namespace_excluded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("scope exclusion not reported: %+v", body)
	}
}

func TestHandleSearchPartialNamespaceCoverage(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "partial-search", []string{"alpha"})
	result, _, err := handleSearch(ctx, nil, searchInput{Query: "kind:Pod ns:alpha ns:beta", Context: "none"})
	if err != nil {
		t.Fatal(err)
	}
	var body search.Result
	if err = json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, gap := range body.Unsearched {
		if gap.Kind == "Pod" && gap.Reason == "namespace_excluded" {
			found = true
		}
	}
	if !body.Partial || !found || len(body.Hits) != 1 || body.Hits[0].Namespace != "alpha" {
		t.Fatalf("partial scope: %+v", body)
	}
}

func TestSearchKindRBACClusterFirstBoundsSARs(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous); getPermCache().Invalidate() })
	ctx := withTestUserPerms(t, "search-count", nil, nil)
	checks := 0
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		checks++
		if namespace != "" {
			t.Errorf("cluster-allowed kind rechecked in %q", namespace)
		}
		return true, nil
	})
	namespaces := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	for _, kind := range search.NamespacedSearchKinds {
		decision, scoped := mcpSearchKindRBAC(ctx, namespaces, kind.Group, kind.Resource)
		if decision != "" || scoped != nil {
			t.Fatalf("allowed kind: %s %v", decision, scoped)
		}
	}
	if checks != 3 {
		t.Fatalf("SAR calls = %d, want 3 sensitive kinds", checks)
	}
}

func TestSearchKindRBACNamespaceCheckFailureRetainsAllowedScope(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous); getPermCache().Invalidate() })
	ctx := withTestUserPerms(t, "search-check-error", nil, []string{"a", "b"})
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		if namespace == "b" {
			return false, fmt.Errorf("SAR unavailable")
		}
		return namespace == "a", nil
	})
	decision, scoped := mcpSearchKindRBAC(ctx, []string{"a", "b"}, "", "secrets")
	if decision != "list_error" || len(scoped) != 1 || scoped[0] != "a" {
		t.Fatalf("SAR failure: %s %v", decision, scoped)
	}
	decision, _ = mcpSearchKindRBAC(ctx, []string{"a"}, "", "unavailable")
	if decision != "" {
		t.Fatalf("fully allowed narrower scope: %s", decision)
	}
}

func TestSearchKindRBACDeniedClusterCountsAndCachesNamespaceChecks(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous); getPermCache().Invalidate() })
	namespaces := []string{"a", "b", "c", "denied"}
	ctx := withTestUserPerms(t, "search-denied-counts", nil, namespaces)
	var checks atomic.Int32
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		checks.Add(1)
		return namespace != "" && namespace != "denied", nil
	})
	for range 2 {
		for _, kind := range search.NamespacedSearchKinds {
			decision, scoped := mcpSearchKindRBAC(ctx, namespaces, kind.Group, kind.Resource)
			if decision != "override" || len(scoped) != 3 {
				t.Fatalf("%s denied-cluster scope: %s %v", kind.Kind, decision, scoped)
			}
		}
		if got, want := checks.Load(), int32(len(search.NamespacedSearchKinds)*(len(namespaces)+1)); got != want {
			t.Fatalf("SAR calls = %d, want %d (one cluster check plus visible namespaces per sensitive kind, cached on repeat)", got, want)
		}
	}
}

func mcpSearch(t *testing.T, ctx context.Context, input searchInput) search.Result {
	t.Helper()
	input.Context, input.Include = "none", "none"
	result, _, err := handleSearch(ctx, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	var body search.Result
	if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestHandleSearchClusterScopedForNamespaceRestrictedCaller(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "search-node-reader", []string{"alpha"})
	grantClusterRead(t, "search-node-reader", "/nodes")
	for _, input := range []searchInput{
		{Query: "kind:Node node"},
		{Query: "node"},
		{Query: "kind:Node node", Namespace: "alpha"},
	} {
		body := mcpSearch(t, ctx, input)
		nodes := 0
		for _, hit := range body.Hits {
			if hit.Kind == "Node" {
				nodes++
			}
		}
		if nodes != 2 {
			t.Fatalf("%+v: restricted caller with list nodes got %d nodes: %+v", input, nodes, body)
		}
	}
	body := mcpSearch(t, ctx, searchInput{Query: "node", Namespace: "alpha"})
	for _, hit := range body.Hits {
		if hit.Kind == "Node" {
			t.Fatalf("broad namespace-scoped search returned a Node: %+v", body.Hits)
		}
	}

	ctx = withRestrictedUser(t, "search-no-nodes", []string{"alpha"})
	denyClusterRead(t, "search-no-nodes", "/nodes")
	body = mcpSearch(t, ctx, searchInput{Query: "kind:Node node"})
	if len(body.Hits) != 0 || !slices.ContainsFunc(body.Unsearched, func(gap search.UnsearchedKind) bool {
		return gap.Kind == "Node" && gap.Reason == "rbac_denied"
	}) {
		t.Fatalf("caller without list nodes: %+v", body)
	}
}

func TestHandleSearchCollapsesExcludedNamespacesAndKeepsKindSpelling(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "search-collapse", []string{"alpha"})
	body := mcpSearch(t, ctx, searchInput{Query: "pod ns:beta ns:gamma"})
	excluded := 0
	for _, gap := range body.Unsearched {
		if gap.Reason == "namespace_excluded" {
			excluded++
			if gap.Kind != "*" || !slices.Equal(gap.Namespaces, []string{"beta", "gamma"}) {
				t.Fatalf("collapsed exclusion: %+v", gap)
			}
		}
	}
	if excluded != 1 || len(body.Hits) != 0 {
		t.Fatalf("broad exclusion entries = %d: %+v", excluded, body)
	}
	body = mcpSearch(t, ctx, searchInput{Query: "kind:NoSuchKindXyz"})
	if !slices.ContainsFunc(body.Unsearched, func(gap search.UnsearchedKind) bool {
		return gap.Kind == "NoSuchKindXyz" && gap.Reason == "not_indexed"
	}) {
		t.Fatalf("unknown kind coverage: %+v", body.Unsearched)
	}
}
