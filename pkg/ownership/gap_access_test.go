package ownership_test

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

type countedAccess struct {
	access
	checks int
}

func (a *countedAccess) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	a.checks++
	return a.access.CanRead(gk, namespace)
}

func TestGapsRadarsSetupCausesFollowNamespaceAccess(t *testing.T) {
	s, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	roles := resourceid.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "Role"}
	volumes := resourceid.GroupKind{Kind: "PersistentVolume"}
	inAlpha := ownership.SearchScope{Namespaces: []string{"alpha", "beta"}}
	everywhere := ownership.SearchScope{AllNamespaces: true, IncludeClusterScoped: true}
	for _, tc := range []struct {
		name       string
		gap        ownership.Gap
		scope      ownership.SearchScope
		want       bool
		wantChecks int
	}{
		{"not collected where the caller sees the namespace", ownership.Gap{Kind: roles, Namespace: "alpha", Reason: readreason.RadarNamespaceNotCollected}, inAlpha, true, 0},
		{"not collected where the caller doesn't", ownership.Gap{Kind: roles, Namespace: "beta", Reason: readreason.RadarNamespaceNotCollected}, inAlpha, false, 0},
		{"Radar forbidden", ownership.Gap{Kind: roles, Namespace: "alpha", Reason: readreason.RadarForbidden}, inAlpha, true, 0},
		{"a read error still needs the kind", ownership.Gap{Kind: roles, Namespace: "alpha", Reason: readreason.ReadError}, inAlpha, false, 1},
		{"a kind not collected anywhere", ownership.Gap{Kind: pods, Reason: readreason.RadarKindNotCollected}, ownership.SearchScope{AllNamespaces: true}, true, 0},
		{"a kind not collected in the scope's namespaces", ownership.Gap{Kind: pods, Reason: readreason.RadarKindNotCollected}, inAlpha, true, 0},
		{"a cluster-scoped kind not collected", ownership.Gap{Kind: volumes, Reason: readreason.RadarKindNotCollected}, everywhere, true, 0},
		{"a cluster-scoped kind Radar may not list", ownership.Gap{Kind: volumes, Reason: readreason.RadarForbidden}, everywhere, true, 0},
		{"a cluster-scoped read error still needs the kind", ownership.Gap{Kind: volumes, Reason: readreason.ReadError}, everywhere, false, 1},
		{"a cluster-scoped kind still loading still needs the kind", ownership.Gap{Kind: volumes, Reason: readreason.KindSyncPending}, everywhere, false, 1},
		{"a kind discovery doesn't serve still needs the kind", ownership.Gap{Kind: resourceid.GroupKind{Group: "example.io", Kind: "Unserved"}, Reason: readreason.RadarKindNotCollected}, everywhere, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &countedAccess{access: access{namespaces: []string{"alpha"}, denied: []resourceid.GroupKind{roles, pods, volumes}}}
			if got := ownership.GapCoversReadable(a, s.Catalog(), tc.gap, tc.scope); got != tc.want {
				t.Errorf("GapCoversReadable = %v, want %v", got, tc.want)
			}
			if a.checks != tc.wantChecks {
				t.Errorf("%d kind checks, want %d", a.checks, tc.wantChecks)
			}
		})
	}
}

// A gap Radar's setup causes on a cluster-scoped kind names no object, so it
// is decided by whether the caller may read any namespace, with no check of
// the kind.
func TestClusterScopedSetupGapFollowsNamespaceAccess(t *testing.T) {
	s, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	gap := ownership.Gap{Kind: resourceid.GroupKind{Kind: "PersistentVolume"}, Reason: readreason.RadarKindNotCollected}
	everywhere := ownership.SearchScope{AllNamespaces: true, IncludeClusterScoped: true}
	for _, tc := range []struct {
		name       string
		namespaces []string
		want       bool
	}{{"every namespace", nil, true}, {"one namespace", []string{"alpha"}, true}, {"no namespace", []string{}, false}} {
		a := &countedAccess{access: access{namespaces: tc.namespaces}}
		if got := ownership.GapCoversReadable(a, s.Catalog(), gap, everywhere); got != tc.want || a.checks != 0 {
			t.Errorf("%s: GapCoversReadable = %v after %d checks, want %v after none", tc.name, got, a.checks, tc.want)
		}
	}
}
