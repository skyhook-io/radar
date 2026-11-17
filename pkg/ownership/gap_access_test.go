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
	inAlpha := ownership.SearchScope{Namespaces: []string{"alpha", "beta"}}
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &countedAccess{access: access{namespaces: []string{"alpha"}, denied: []resourceid.GroupKind{roles, pods}}}
			if got := ownership.GapCoversReadable(a, s.Catalog(), tc.gap, tc.scope); got != tc.want {
				t.Errorf("GapCoversReadable = %v, want %v", got, tc.want)
			}
			if a.checks != tc.wantChecks {
				t.Errorf("%d kind checks, want %d", a.checks, tc.wantChecks)
			}
		})
	}
}
