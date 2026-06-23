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
