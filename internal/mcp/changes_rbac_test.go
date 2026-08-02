package mcp

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// filterRecentChangesRBAC must not become a side channel around per-kind RBAC:
// a user who can see a namespace but not a specific kind in it must not receive
// that kind's change rows, and cluster-scoped kinds they can't list must drop —
// while readable kinds, helm-sourced rows, and the no-per-user-RBAC case pass.
func TestFilterRecentChangesRBAC(t *testing.T) {
	changes := []issuesapi.RecentChange{
		{Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1", Name: "pod-policy"}, // cluster-scoped
		{Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web"},                               // namespaced, readable
		{Kind: "Secret", APIVersion: "v1", Namespace: "shop", Name: "db"},                                         // namespaced, NOT readable
		{Kind: "HelmRelease", Source: helmChangeSource, Namespace: "shop", Name: "app"},                           // synthesized helm → passthrough
	}
	clone := func() []issuesapi.RecentChange { return append([]issuesapi.RecentChange(nil), changes...) }
	has := func(got []issuesapi.RecentChange, kind string) bool {
		for _, c := range got {
			if c.Kind == kind {
				return true
			}
		}
		return false
	}

	// No per-user RBAC on context (radar-SA / benchmark): nothing is filtered.
	if got := filterRecentChangesRBAC(context.Background(), clone()); len(got) != len(changes) {
		t.Fatalf("no-user context must keep all changes, got %+v", got)
	}

	// User can list deployments in shop, but NOT secrets there and NOT webhooks
	// cluster-wide (unseeded → SAR against a nil client → fail closed).
	ctx := withClusterAdmin(t, "scoped")
	perms := getPermCache().Get("scoped")
	perms.SetCanI("list", "apps", "deployments", "shop", true)

	got := filterRecentChangesRBAC(ctx, clone())
	if has(got, "MutatingWebhookConfiguration") {
		t.Fatalf("cluster-scoped webhook leaked to a user without RBAC: %+v", got)
	}
	if has(got, "Secret") {
		t.Fatalf("Gap A: unreadable Secret leaked in an allowed namespace: %+v", got)
	}
	if !has(got, "Deployment") {
		t.Fatalf("readable Deployment must survive the per-kind filter: %+v", got)
	}
	if !has(got, "HelmRelease") {
		t.Fatalf("helm-sourced row must pass through (gated upstream): %+v", got)
	}
}
