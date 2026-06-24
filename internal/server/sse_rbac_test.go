package server

import (
	"testing"

	topology "github.com/skyhook-io/radar/pkg/topology"
)

// deniedKindsKey is the grouping seam that keeps a user who is denied a
// cluster-scoped topology kind from sharing a more-privileged peer's
// pre-marshaled (un-stripped) frame. It must be empty for full access (the
// common case, so those users still coalesce) and stable + sorted otherwise.
func TestDeniedKindsKey(t *testing.T) {
	if got := deniedKindsKey(nil); got != "" {
		t.Fatalf("nil → %q, want empty (full-access users must coalesce)", got)
	}
	if got := deniedKindsKey(map[topology.NodeKind]bool{}); got != "" {
		t.Fatalf("empty → %q, want empty", got)
	}
	if got := deniedKindsKey(map[topology.NodeKind]bool{"Node": true}); got != "Node" {
		t.Fatalf("single → %q, want Node", got)
	}

	// Same set, different insertion order → identical key (map iteration order
	// must not leak into grouping, or two equally-denied users would build
	// duplicate frames).
	a := deniedKindsKey(map[topology.NodeKind]bool{"StorageClass": true, "Node": true, "PersistentVolume": true})
	b := deniedKindsKey(map[topology.NodeKind]bool{"Node": true, "PersistentVolume": true, "StorageClass": true})
	if a != b {
		t.Fatalf("unstable key across iteration order: %q vs %q", a, b)
	}
	if a != "Node,PersistentVolume,StorageClass" {
		t.Fatalf("key = %q, want Node,PersistentVolume,StorageClass", a)
	}
}

// clientCanSeeChange gates k8s_event (diff-bearing) frames per client so a
// restricted user doesn't receive change content for namespaces or cluster-scoped
// kinds their RBAC forbids.
func TestClientCanSeeChange(t *testing.T) {
	allAccess := ClientInfo{Namespaces: nil}
	scopedAB := ClientInfo{Namespaces: []string{"a", "b"}}
	noAccess := ClientInfo{Namespaces: []string{"__no_access__"}}
	deniedNodes := ClientInfo{DeniedKinds: map[topology.NodeKind]bool{"Node": true}}
	// A user who can't list namespaces has Namespace added to the deny set at
	// subscribe (handleSSE), so Namespace change events (cluster-scoped, name="")
	// are blocked.
	deniedNamespaces := ClientInfo{Namespaces: []string{"a"}, DeniedKinds: map[topology.NodeKind]bool{"Namespace": true}}

	cases := []struct {
		name      string
		info      ClientInfo
		namespace string
		kind      string
		want      bool
	}{
		{"all-access sees namespaced change", allAccess, "a", "ConfigMap", true},
		{"scoped sees allowed namespace", scopedAB, "a", "Deployment", true},
		{"scoped does NOT see other namespace", scopedAB, "c", "Deployment", false},
		{"no-access sees nothing namespaced", noAccess, "a", "ConfigMap", false},
		{"cluster-scoped allowed when not denied", scopedAB, "", "Node", true},
		{"cluster-scoped denied kind blocked", deniedNodes, "", "Node", false},
		{"cluster-scoped non-denied kind allowed", deniedNodes, "", "StorageClass", true},
		{"Namespace event blocked when can't list namespaces", deniedNamespaces, "", "Namespace", false},
		{"Namespace event allowed when can list namespaces", scopedAB, "", "Namespace", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientCanSeeChange(tc.info, tc.namespace, tc.kind); got != tc.want {
				t.Fatalf("clientCanSeeChange(%v, %q, %q) = %v, want %v", tc.info, tc.namespace, tc.kind, got, tc.want)
			}
		})
	}
}
