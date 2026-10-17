package ownershiplive

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Every form Argo CD accepts for the in-cluster destination is local.
func TestLiveMembershipKnowsTheInClusterDestination(t *testing.T) {
	scope := func(resourceid.GroupKind) (bool, bool) { return true, true }
	status := map[string]any{"reconciledAt": "t"}
	for _, destination := range []map[string]any{
		{"server": "https://kubernetes.default.svc"},
		{"server": "https://kubernetes.default.svc:443"},
		{"server": "https://kubernetes.default.svc.cluster.local"},
		{"server": "https://KUBERNETES.default.svc./"},
		{"server": "kubernetes.default"},
		{"server": " https://kubernetes "},
		{"name": "in-cluster"},
		{"name": " In-Cluster "},
	} {
		if state, _, _ := argoMembership(map[string]any{"destination": destination}, status, scope); state != ownership.MembersRecorded {
			t.Errorf("%v: %v, want local", destination, state)
		}
	}
	for _, destination := range []map[string]any{
		{"server": "https://prod.example.com:6443"},
		{"server": "https://kubernetes.example.com"},
		{"name": "prod"},
	} {
		if state, _, _ := argoMembership(map[string]any{"destination": destination}, status, scope); state != ownership.MembersRemote {
			t.Errorf("%v: %v, want remote", destination, state)
		}
	}
}

func TestLiveMembershipReadsControllerShapes(t *testing.T) {
	scope := func(gk resourceid.GroupKind) (bool, bool) {
		switch gk.Kind {
		case "Service", "Deployment":
			return true, true
		case "ClusterRole":
			return false, true
		}
		return false, false
	}
	local := map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"}}
	for _, tc := range []struct {
		name    string
		gk      resourceid.GroupKind
		spec    map[string]any
		status  map[string]any
		state   ownership.MembershipState
		members []resourceid.Ref
	}{
		{"Argo CD", ownership.ArgoApplication, local, map[string]any{"resources": []any{
			map[string]any{"kind": "Service", "name": "ui", "namespace": "shop", "version": "v1"},
			map[string]any{"group": "apps", "kind": "Deployment", "name": "ui", "version": "v1"},
			map[string]any{"group": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "reader"},
		}}, ownership.MembersRecorded, []resourceid.Ref{
			{Kind: "Service", Namespace: "shop", Name: "ui"},
			{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "ui"},
			{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "reader"},
		}},
		{"Argo CD, reconciled empty", ownership.ArgoApplication, local, map[string]any{"reconciledAt": "t"}, ownership.MembersRecorded, []resourceid.Ref{}},
		{"Argo CD, not reconciled", ownership.ArgoApplication, local, nil, ownership.MembersUnreadable, nil},
		{"Argo CD, elsewhere", ownership.ArgoApplication, map[string]any{"destination": map[string]any{"name": "prod"}}, map[string]any{"resources": []any{
			map[string]any{"kind": "Service", "name": "ui", "namespace": "shop"},
		}}, ownership.MembersRemote, []resourceid.Ref{{Kind: "Service", Namespace: "shop", Name: "ui"}}},
		{"Argo CD, elsewhere, not reconciled", ownership.ArgoApplication, map[string]any{"destination": map[string]any{"name": "prod"}}, nil, ownership.MembersUnreadable, nil},
		{"Flux", ownership.FluxKustomization, nil, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_ui__Service"},
			map[string]any{"id": "shop_reader_rbac.authorization.k8s.io_ClusterRole"},
		}}}, ownership.MembersRecorded, []resourceid.Ref{
			{Kind: "Service", Namespace: "shop", Name: "ui"},
			{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "reader"},
		}},
		{"Flux, a bad ID", ownership.FluxHelmRelease, nil, map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "ui"}}}}, ownership.MembersUnreadable, nil},
		{"Flux, no inventory", ownership.FluxHelmRelease, nil, map[string]any{}, ownership.MembersUnreadable, nil},
		{"Flux, elsewhere", ownership.FluxKustomization, map[string]any{"kubeConfig": map[string]any{}}, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_ui__Service"},
		}}}, ownership.MembersRemote, []resourceid.Ref{{Kind: "Service", Namespace: "shop", Name: "ui"}}},
		{"Flux, elsewhere, no inventory", ownership.FluxKustomization, map[string]any{"kubeConfig": map[string]any{}}, nil, ownership.MembersUnreadable, nil},
		{"Flux, the core group spelled out", ownership.FluxKustomization, nil, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_ui_core_Service"},
		}}}, ownership.MembersRecorded, []resourceid.Ref{{Kind: "Service", Namespace: "shop", Name: "ui"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{}}
			if tc.spec != nil {
				u.Object["spec"] = tc.spec
			}
			if tc.status != nil {
				u.Object["status"] = tc.status
			}
			state, members, _ := liveMembership(tc.gk, u, scope)
			want := sortedRefs(slices.Clone(tc.members))
			if state != tc.state || !slices.Equal(members, want) || (members == nil) != (want == nil) {
				t.Fatalf("got %v %v, want %v %v", state, members, tc.state, want)
			}
		})
	}
}
