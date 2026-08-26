package upgrade

import (
	"context"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

type fakeUpgradeAuthorizer struct {
	namespaces  []string
	canList     map[string]bool
	filterCalls []string
	filtered    []string
}

func (f *fakeUpgradeAuthorizer) Namespaces() []string { return f.namespaces }
func (f *fakeUpgradeAuthorizer) CanList(group, resource, namespace string) bool {
	return f.canList[group+"/"+resource+"/"+namespace]
}
func (f *fakeUpgradeAuthorizer) CanGetSubresource(group, resource, subresource string) bool {
	return false
}
func (f *fakeUpgradeAuthorizer) FilterNamespacesByCanList(group, resource string, namespaces []string) []string {
	f.filterCalls = append(f.filterCalls, group+"/"+resource)
	return f.filtered
}

func TestResolveHelmNamespacesForAuthorizerDecisions(t *testing.T) {
	ctx := context.Background()

	t.Run("no namespaced access refuses helm collection", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{namespaces: []string{}}
		if _, ok := ResolveHelmNamespaces(ctx, authz, []string{}); ok {
			t.Fatal("no-access scope must refuse Helm collection entirely")
		}
	})

	t.Run("scoped ceiling passes through unchanged", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{}
		got, ok := ResolveHelmNamespaces(ctx, authz, []string{"team-a"})
		if !ok || !slices.Equal(got, []string{"team-a"}) {
			t.Fatalf("scoped resolution = (%v, %v), want the ceiling unchanged", got, ok)
		}
	})

	t.Run("authenticated cluster-wide without secrets narrows to secret-listable namespaces", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{
			canList:  map[string]bool{"/secrets/": false},
			filtered: []string{"team-a"},
		}
		ctxUser := auth.ContextWithUser(ctx, &auth.User{Username: "alice"})
		got, ok := ResolveHelmNamespaces(ctxUser, authz, nil)
		if !ok || !slices.Equal(got, []string{"team-a"}) {
			t.Fatalf("secrets-narrowed resolution = (%v, %v), want [team-a]", got, ok)
		}
		if !slices.Contains(authz.filterCalls, "/secrets") {
			t.Fatalf("filter calls = %v, want a per-namespace secrets filter", authz.filterCalls)
		}
	})

	t.Run("authenticated cluster-wide with secrets stays cluster-wide", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{canList: map[string]bool{"/secrets/": true}}
		ctxUser := auth.ContextWithUser(ctx, &auth.User{Username: "alice"})
		got, ok := ResolveHelmNamespaces(ctxUser, authz, nil)
		if !ok || got != nil {
			t.Fatalf("cluster-wide resolution = (%v, %v), want nil (cluster-wide)", got, ok)
		}
		if len(authz.filterCalls) != 0 {
			t.Fatalf("filter calls = %v, want none when cluster-wide secrets access exists", authz.filterCalls)
		}
	})
}
