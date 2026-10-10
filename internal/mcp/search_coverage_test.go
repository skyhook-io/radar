package mcp

import (
	"context"
	"encoding/json"
	"testing"

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
