package ownershiplive

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// A built-in kind keeps its own scope unless discovery disputes it; a kind
// two resources serve has none; any other kind has discovery's.
func TestDiscoveredScope(t *testing.T) {
	gc := []string{"get", "list", "watch"}
	served := []ownership.ServedResource{
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole", Resource: "clusterroles", Namespaced: true, Verbs: gc},
		{Group: "example.io", Version: "v1", Kind: "Widget", Resource: "widgets", Namespaced: true, Verbs: gc},
		{Group: "example.io", Version: "v1", Kind: "Gadget", Resource: "gadgets", Namespaced: true, Verbs: gc},
		{Group: "example.io", Version: "v1beta1", Kind: "Gadget", Resource: "gizmos", Verbs: gc},
		{Group: "example.io", Version: "v1", Kind: "Dial", Resource: "dials", Namespaced: true, Verbs: gc},
		{Group: "example.io", Version: "v1beta1", Kind: "Dial", Resource: "dials", Verbs: gc},
		{Version: "v1", Kind: "ConfigMap", Resource: "configmaps", Namespaced: true, Verbs: gc},
	}
	reversed := slices.Clone(served)
	slices.Reverse(reversed)
	for _, order := range [][]ownership.ServedResource{served, reversed} {
		scope := discoveredScope(order)
		for _, tc := range []struct {
			gk                resourceid.GroupKind
			namespaced, known bool
		}{
			{resourceid.GroupKind{Kind: "ConfigMap"}, true, true},
			{resourceid.GroupKind{Kind: "Node"}, false, true},
			{resourceid.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}, false, false},
			{resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, true, true},
			{resourceid.GroupKind{Group: "example.io", Kind: "Gadget"}, false, false},
			{resourceid.GroupKind{Group: "example.io", Kind: "Dial"}, false, false},
			{resourceid.GroupKind{Group: "example.io", Kind: "Sprocket"}, false, false},
		} {
			if namespaced, known := scope(tc.gk); namespaced != tc.namespaced || known != tc.known {
				t.Errorf("%v: namespaced %v known %v, want %v %v", tc.gk, namespaced, known, tc.namespaced, tc.known)
			}
		}
	}
}

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
		{"Argo CD, an empty list written, no reconciledAt", ownership.ArgoApplication, local, map[string]any{"resources": []any{}}, ownership.MembersRecorded, []resourceid.Ref{}},
		{"Argo CD, comparison failed", ownership.ArgoApplication, local, map[string]any{"reconciledAt": "t", "resources": []any{}, "conditions": []any{map[string]any{"type": "ComparisonError"}}}, ownership.MembersUnreadable, nil},
		{"Argo CD, comparison unknown", ownership.ArgoApplication, local, map[string]any{"reconciledAt": "t", "sync": map[string]any{"status": "Unknown"}}, ownership.MembersUnreadable, nil},
		{"Argo CD, elsewhere", ownership.ArgoApplication, map[string]any{"destination": map[string]any{"name": "prod"}}, map[string]any{"resources": []any{
			map[string]any{"kind": "Service", "name": "ui", "namespace": "shop"},
		}}, ownership.MembersRemote, []resourceid.Ref{{Kind: "Service", Namespace: "shop", Name: "ui"}}},
		{"Argo CD, elsewhere, not reconciled", ownership.ArgoApplication, map[string]any{"destination": map[string]any{"name": "prod"}}, nil, ownership.MembersRemote, []resourceid.Ref{}},
		{"Argo CD, moved here but compared elsewhere", ownership.ArgoApplication, local, map[string]any{
			"sync":      map[string]any{"comparedTo": map[string]any{"destination": map[string]any{"name": "prod", "namespace": "shop"}}},
			"resources": []any{map[string]any{"kind": "Service", "name": "ui", "namespace": "shop"}},
		}, ownership.MembersRemote, []resourceid.Ref{{Kind: "Service", Namespace: "shop", Name: "ui"}}},
		{"Argo CD, moved elsewhere but compared here", ownership.ArgoApplication, map[string]any{"destination": map[string]any{"name": "prod"}}, map[string]any{
			"sync":      map[string]any{"comparedTo": map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"}}},
			"resources": []any{map[string]any{"kind": "Service", "name": "ui"}},
		}, ownership.MembersRecorded, []resourceid.Ref{{Kind: "Service", Namespace: "shop", Name: "ui"}}},
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
		{"Flux, elsewhere, no inventory", ownership.FluxKustomization, map[string]any{"kubeConfig": map[string]any{}}, nil, ownership.MembersRemote, []resourceid.Ref{}},
		{"Flux, the core group spelled out", ownership.FluxKustomization, nil, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_ui_core_Service"},
		}}}, ownership.MembersRecorded, []resourceid.Ref{{Kind: "Service", Namespace: "shop", Name: "ui"}}},
		{"Flux, a colon in an RBAC name", ownership.FluxHelmRelease, nil, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "_system__metrics-server_rbac.authorization.k8s.io_ClusterRole"},
		}}}, ownership.MembersRecorded, []resourceid.Ref{{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:metrics-server"}}},
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
