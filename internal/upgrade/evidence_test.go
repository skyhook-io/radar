package upgrade

import (
	"context"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

type fakeUpgradeAuthorizer struct {
	namespaces        []string
	canList           map[string]bool
	listDecisions     map[string]EvidenceAuthorizationDecision
	nodeProxyDecision *EvidenceAuthorizationDecision
	filterCalls       []string
	filtered          []string
	// filterUnchecked makes the per-namespace filter report a check it
	// couldn't answer.
	filterUnchecked bool
}

func (f *fakeUpgradeAuthorizer) Namespaces() []string { return f.namespaces }
func (f *fakeUpgradeAuthorizer) CanList(group, resource, namespace string) EvidenceAuthorizationDecision {
	key := group + "/" + resource + "/" + namespace
	if decision, ok := f.listDecisions[key]; ok {
		return decision
	}
	return EvidenceAuthorizationDecision{Allowed: f.canList[key], Authoritative: true}
}
func (f *fakeUpgradeAuthorizer) CanGetSubresource(group, resource, subresource string) EvidenceAuthorizationDecision {
	if f.nodeProxyDecision != nil {
		return *f.nodeProxyDecision
	}
	return EvidenceAuthorizationDecision{Authoritative: true}
}
func (f *fakeUpgradeAuthorizer) FilterNamespacesByCanList(group, resource string, namespaces []string) ([]string, bool) {
	f.filterCalls = append(f.filterCalls, group+"/"+resource)
	return f.filtered, !f.filterUnchecked
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

	t.Run("an unanswered secrets check is reported, not passed off as a denial", func(t *testing.T) {
		ctxUser := auth.ContextWithUser(ctx, &auth.User{Username: "alice"})
		for _, filtered := range [][]string{{"team-a"}, nil} {
			authz := &fakeUpgradeAuthorizer{canList: map[string]bool{"/secrets/": false}, filtered: filtered, filterUnchecked: true}
			if _, ok, checked := ResolveHelmNamespacesChecked(ctxUser, authz, nil); !ok || checked {
				t.Fatalf("filtered %v: ok=%v checked=%v, want ok and unchecked", filtered, ok, checked)
			}
		}
		authz := &fakeUpgradeAuthorizer{canList: map[string]bool{"/secrets/": false}, filtered: []string{"team-a"}}
		if _, _, checked := ResolveHelmNamespacesChecked(ctxUser, authz, nil); !checked {
			t.Fatal("every check answered, want checked")
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

func TestResolveCachedEvidenceNamespaces(t *testing.T) {
	t.Run("cluster-wide grant preserves cluster-wide scope", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{canList: map[string]bool{"/configmaps/": true}}
		if got := resolveCachedEvidenceNamespaces(authz, "", "configmaps", nil); got != nil {
			t.Fatalf("scope = %v, want nil cluster-wide scope", got)
		}
	})

	t.Run("cluster-wide scan narrows to resource-specific grants", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{canList: map[string]bool{"/configmaps/": false}, filtered: []string{"team-a"}}
		if got := resolveCachedEvidenceNamespaces(authz, "", "configmaps", nil); !slices.Equal(got, []string{"team-a"}) {
			t.Fatalf("scope = %v, want [team-a]", got)
		}
		if !slices.Contains(authz.filterCalls, "/configmaps") {
			t.Fatalf("filter calls = %v, want configmaps authorization", authz.filterCalls)
		}
	})

	t.Run("no resource grant remains explicitly empty", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{canList: map[string]bool{"/persistentvolumeclaims/": false}}
		got := resolveCachedEvidenceNamespaces(authz, "", "persistentvolumeclaims", []string{"team-a"})
		if got == nil || len(got) != 0 {
			t.Fatalf("scope = %#v, want explicit empty scope", got)
		}
	})
}
