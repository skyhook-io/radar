package gitops

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestClaims(t *testing.T) {
	cm := resourceid.Ref{Kind: "ConfigMap", Namespace: "shop", Name: "settings"}
	app := func(ns, name string) resourceid.Ref {
		return resourceid.Ref{Group: "argoproj.io", Kind: "Application", Namespace: ns, Name: name}
	}
	for _, tc := range []struct {
		name        string
		labels      map[string]string
		annotations map[string]string
		want        []Claim
	}{
		{name: "no metadata"},
		{
			name:        "tracking ID on its own object",
			annotations: map[string]string{argoTrackingIDAnnotation: "shop:/ConfigMap:shop/settings"},
			want:        []Claim{{Manager: app("", "shop"), Source: ClaimArgoTrackingID}},
		},
		{
			name:        "tracking ID of an Application outside Argo CD's namespace",
			annotations: map[string]string{argoTrackingIDAnnotation: "team_shop:/ConfigMap:shop/settings"},
			want:        []Claim{{Manager: app("team", "shop"), Source: ClaimArgoTrackingID}},
		},
		{
			// A Secret copied with its source's annotations names the source.
			name:        "tracking ID copied from another object",
			annotations: map[string]string{argoTrackingIDAnnotation: "shop:/ConfigMap:other/settings"},
			want:        []Claim{{Manager: app("", "shop"), Source: ClaimArgoTrackingID, Copied: true}},
		},
		{
			name:        "malformed tracking ID makes no claim",
			annotations: map[string]string{argoTrackingIDAnnotation: "shop:garbage"},
		},
		{
			name:   "instance label, with and without a namespace",
			labels: map[string]string{argoInstanceLabel: "team_shop"},
			want:   []Claim{{Manager: app("team", "shop"), Source: ClaimArgoInstanceLabel}},
		},
		{
			name:   "Flux labels need both name and namespace",
			labels: map[string]string{fluxKustomizeNameLabel: "apps", fluxHelmNameLabel: "web", fluxHelmNSLabel: "flux-system"},
			want: []Claim{{
				Manager: resourceid.Ref{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "flux-system", Name: "web"},
				Source:  ClaimFluxHelmRelease,
			}},
		},
		{
			// Every candidate survives, so a resolver can see the disagreement.
			name:        "several sources",
			labels:      map[string]string{fluxKustomizeNameLabel: "apps", fluxKustomizeNSLabel: "flux-system", argoInstanceLabel: "shop"},
			annotations: map[string]string{helmReleaseNameAnno: "web", helmReleaseNSAnno: "shop"},
			want: []Claim{
				{Manager: resourceid.Ref{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}, Source: ClaimFluxKustomization},
				{Manager: app("", "shop"), Source: ClaimArgoInstanceLabel},
				{Manager: resourceid.Ref{Kind: "HelmRelease", Namespace: "shop", Name: "web"}, Source: ClaimHelmRelease},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Claims(cm, tc.labels, tc.annotations); !slices.Equal(got, tc.want) {
				t.Fatalf("Claims = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// Argo CD writes its destination namespace into a cluster-scoped object's
// tracking ID, or none, and ignores it when comparing.
func TestTrackingIDOfAClusterScopedObject(t *testing.T) {
	role := resourceid.Ref{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "reader"}
	for _, id := range []string{"shop:rbac.authorization.k8s.io/ClusterRole:/reader", "shop:rbac.authorization.k8s.io/ClusterRole:shop/reader"} {
		if got := Claims(role, nil, map[string]string{argoTrackingIDAnnotation: id}); len(got) != 1 || got[0].Copied {
			t.Errorf("Claims(%s) = %+v; want one claim on its own object", id, got)
		}
	}
	if got := Claims(role, nil, map[string]string{argoTrackingIDAnnotation: "shop:rbac.authorization.k8s.io/ClusterRole:/writer"}); len(got) != 1 || !got[0].Copied {
		t.Errorf("Claims naming another ClusterRole = %+v; want copied", got)
	}
}
