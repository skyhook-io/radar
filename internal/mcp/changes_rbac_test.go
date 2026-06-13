package mcp

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// The change feed must not become a side channel around the per-kind
// cluster-scoped SAR that gates every other read: a cluster-wide user without
// RBAC for admission webhook configs must not see them via get_changes, while
// namespaced changes and full-access (no per-user RBAC) are unaffected.
func TestFilterChangesByClusterScopedRBAC(t *testing.T) {
	changes := []issuesapi.RecentChange{
		{Kind: "MutatingWebhookConfiguration", Name: "pod-policy"}, // cluster-scoped
		{Kind: "Deployment", Namespace: "shop", Name: "web"},       // namespaced
	}
	has := func(got []issuesapi.RecentChange, kind string) bool {
		for _, c := range got {
			if c.Kind == kind {
				return true
			}
		}
		return false
	}

	// No per-user RBAC on context (radar-SA / benchmark): nothing is filtered.
	if got := filterChangesByClusterScopedRBAC(context.Background(), append([]issuesapi.RecentChange(nil), changes...)); len(got) != 2 {
		t.Fatalf("no-user context must keep all changes, got %+v", got)
	}

	// Cluster-wide user WITHOUT webhook RBAC: the webhook change is dropped (the
	// seeded SAR cache has no grant → canReadClusterScopedKind fails closed),
	// the namespaced change is kept (gated by namespace, not this check).
	denied := withClusterAdmin(t, "no-webhook-rbac")
	got := filterChangesByClusterScopedRBAC(denied, append([]issuesapi.RecentChange(nil), changes...))
	if has(got, "MutatingWebhookConfiguration") {
		t.Fatalf("cluster-scoped webhook change leaked to a user without RBAC: %+v", got)
	}
	if !has(got, "Deployment") {
		t.Fatalf("namespaced change must survive the cluster-scoped filter: %+v", got)
	}

	// Same user, now granted webhook read: the webhook change is returned.
	granted := withClusterAdmin(t, "has-webhook-rbac")
	grantClusterRead(t, "has-webhook-rbac", "admissionregistration.k8s.io/mutatingwebhookconfigurations")
	if got := filterChangesByClusterScopedRBAC(granted, append([]issuesapi.RecentChange(nil), changes...)); !has(got, "MutatingWebhookConfiguration") {
		t.Fatalf("granted user must see the webhook change, got %+v", got)
	}
}
