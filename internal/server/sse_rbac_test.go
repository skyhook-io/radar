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

func TestNodeClassAuthorizationGroupsUseExactProviderResource(t *testing.T) {
	eksTuple := topology.SARTuple{Group: "eks.amazonaws.com", Resource: "nodeclasses"}
	customTuple := topology.SARTuple{Group: "infra.example.io", Resource: "customnodeclasses"}
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "nodepool//default", Kind: topology.KindNodePool, Name: "default"},
			{ID: "nodeclass//shared/eks.amazonaws.com/nodeclass", Kind: topology.KindNodeClass, Name: "shared", Data: map[string]any{"apiVersion": "eks.amazonaws.com/v1", "resource": "nodeclasses"}},
			{ID: "nodeclass//shared/infra.example.io/customnodeclass", Kind: topology.KindNodeClass, Name: "shared", Data: map[string]any{"apiVersion": "infra.example.io/v1", "resource": "customnodeclasses"}},
		},
		Edges: []topology.Edge{
			{Source: "nodepool//default", Target: "nodeclass//shared/eks.amazonaws.com/nodeclass", Type: topology.EdgeConfigures},
			{Source: "nodepool//default", Target: "nodeclass//shared/infra.example.io/customnodeclass", Type: topology.EdgeConfigures},
		},
	}

	allowed := authorizedNodeClassTuples(topo, func(tuple topology.SARTuple) bool {
		return tuple == eksTuple
	})
	if !allowed[eksTuple] || allowed[customTuple] {
		t.Fatalf("allowed tuples = %+v, want EKS only", allowed)
	}
	if nodeClassTuplesKey(allowed) == nodeClassTuplesKey(map[topology.SARTuple]bool{customTuple: true}) {
		t.Fatal("different provider grants collapsed to one SSE authorization group")
	}

	filtered := cloneTopology(topo)
	filtered.StripNodeClassesExcept(allowed)
	if len(filtered.Nodes) != 2 || len(filtered.Edges) != 1 || filtered.Edges[0].Target != "nodeclass//shared/eks.amazonaws.com/nodeclass" {
		t.Fatalf("exact provider filter produced nodes=%+v edges=%+v", filtered.Nodes, filtered.Edges)
	}
	if len(topo.Nodes) != 3 || len(topo.Edges) != 2 {
		t.Fatal("filtering one SSE authorization group mutated the shared base topology")
	}
}

// clientCanSeeChange gates k8s_event (diff-bearing) frames per client. Without
// an authorizer wired (auth off / tests) it falls back to the namespace +
// topology-denied-kind gate; with one it authorizes the exact (group, resource)
// against the client's RBAC — closing the two gaps a namespace-only gate leaves:
// namespaced kinds unreadable within an allowed namespace, and cluster-scoped
// kinds outside the topology denied set.
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
		group     string
		resource  string
		kind      string
		want      bool
	}{
		// --- Fallback path (Authorize == nil): namespace + denied-kind gate ---
		{"all-access sees namespaced change", allAccess, "a", "", "configmaps", "ConfigMap", true},
		{"scoped sees allowed namespace", scopedAB, "a", "apps", "deployments", "Deployment", true},
		{"scoped does NOT see other namespace", scopedAB, "c", "apps", "deployments", "Deployment", false},
		{"no-access sees nothing namespaced", noAccess, "a", "", "configmaps", "ConfigMap", false},
		{"cluster-scoped allowed when not denied", scopedAB, "", "", "nodes", "Node", true},
		{"cluster-scoped denied kind blocked", deniedNodes, "", "", "nodes", "Node", false},
		{"cluster-scoped non-denied kind allowed", deniedNodes, "", "storage.k8s.io", "storageclasses", "StorageClass", true},
		{"Namespace event blocked when can't list namespaces", deniedNamespaces, "", "", "namespaces", "Namespace", false},
		{"Namespace event allowed when can list namespaces", scopedAB, "", "", "namespaces", "Namespace", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientCanSeeChange(tc.info, tc.namespace, tc.group, tc.resource, tc.kind); got != tc.want {
				t.Fatalf("clientCanSeeChange(%v, %q, %q, %q, %q) = %v, want %v", tc.info, tc.namespace, tc.group, tc.resource, tc.kind, got, tc.want)
			}
		})
	}
}

// TestClientCanSeeChange_SensitiveKindsSuperset proves the authorizer path is a
// functional superset of the interim per-kind SSE gate (the standalone
// sensitive-kinds fix): the SAME kinds — Secret, Role, RoleBinding, ClusterRole,
// ClusterRoleBinding, and both admission webhook configs — are dropped for a
// user who can see the namespace but not read that kind, while ordinary readable
// kinds pass. Ours is per-(kind,namespace) exact rather than cluster-wide, so it
// also gates these inside individual namespaces (no over-denial of a user who
// legitimately holds a per-namespace grant) and covers every other kind too.
func TestClientCanSeeChange_SensitiveKindsSuperset(t *testing.T) {
	// Restricted user: sees ns "a" and reads deployments there, but not the
	// sensitive kinds (namespaced ones denied in "a", cluster-scoped ones at "").
	denied := map[string]bool{
		"|secrets|a":                                                    true,
		"rbac.authorization.k8s.io|roles|a":                             true,
		"rbac.authorization.k8s.io|rolebindings|a":                      true,
		"rbac.authorization.k8s.io|clusterroles|":                       true,
		"rbac.authorization.k8s.io|clusterrolebindings|":                true,
		"admissionregistration.k8s.io|mutatingwebhookconfigurations|":   true,
		"admissionregistration.k8s.io|validatingwebhookconfigurations|": true,
	}
	authorize := func(group, resource, namespace, verb string) bool {
		return !denied[group+"|"+resource+"|"+namespace]
	}
	info := ClientInfo{Namespaces: []string{"a"}, Authorize: authorize}

	cases := []struct {
		kind, namespace, group, resource string
		want                             bool
	}{
		{"Secret", "a", "", "secrets", false},
		{"Role", "a", "rbac.authorization.k8s.io", "roles", false},
		{"RoleBinding", "a", "rbac.authorization.k8s.io", "rolebindings", false},
		{"ClusterRole", "", "rbac.authorization.k8s.io", "clusterroles", false},
		{"ClusterRoleBinding", "", "rbac.authorization.k8s.io", "clusterrolebindings", false},
		{"MutatingWebhookConfiguration", "", "admissionregistration.k8s.io", "mutatingwebhookconfigurations", false},
		{"ValidatingWebhookConfiguration", "", "admissionregistration.k8s.io", "validatingwebhookconfigurations", false},
		{"Deployment", "a", "apps", "deployments", true}, // ordinary readable kind unaffected
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			if got := clientCanSeeChange(info, tc.namespace, tc.group, tc.resource, tc.kind); got != tc.want {
				t.Fatalf("%s in ns %q: got %v, want %v", tc.kind, tc.namespace, got, tc.want)
			}
		})
	}
}

// TestClientCanSeeChange_Authorizer covers the auth-enabled path: the per-kind
// SubjectAccessReview closure, not the namespace-only fallback, decides. This is
// the fix for the two RBAC gaps in the live k8s_event stream.
func TestClientCanSeeChange_Authorizer(t *testing.T) {
	// authorize allows only (group,resource,namespace) tuples in `allowed`.
	allowed := map[string]bool{
		"|pods|a":       true, // core pods in ns a
		"|configmaps|a": true,
		"rbac.authorization.k8s.io|clusterroles|": true, // cluster-scoped clusterroles
	}
	authorize := func(group, resource, namespace, verb string) bool {
		return allowed[group+"|"+resource+"|"+namespace]
	}
	// Client can SEE namespaces a and b (namespace gate passes), but per-kind
	// RBAC is narrower than that within them.
	info := ClientInfo{Namespaces: []string{"a", "b"}, Authorize: authorize}

	cases := []struct {
		name            string
		namespace       string
		group, resource string
		kind            string
		want            bool
	}{
		// Gap A: namespace is visible, but the kind is not readable there.
		{"pods in a allowed", "a", "", "pods", "Pod", true},
		{"secrets in a denied (list-pods-only viewer)", "a", "", "secrets", "Secret", false},
		{"configmaps in a allowed", "a", "", "configmaps", "ConfigMap", true},
		{"pods in b denied (no per-kind grant there)", "b", "", "pods", "Pod", false},
		{"namespace outside client set denied before SAR", "c", "", "pods", "Pod", false},
		// Gap B: cluster-scoped kind outside the topology denied set.
		{"clusterroles allowed by SAR", "", "rbac.authorization.k8s.io", "clusterroles", "ClusterRole", true},
		{"webhookconfigs denied by SAR", "", "admissionregistration.k8s.io", "validatingwebhookconfigurations", "ValidatingWebhookConfiguration", false},
		// Unresolved kind (empty resource) fails closed under auth.
		{"unresolved namespaced kind fails closed", "a", "", "", "MysteryCRD", false},
		{"unresolved cluster-scoped kind fails closed", "", "", "", "MysteryCRD", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientCanSeeChange(info, tc.namespace, tc.group, tc.resource, tc.kind); got != tc.want {
				t.Fatalf("clientCanSeeChange(ns=%q, %q/%q) = %v, want %v", tc.namespace, tc.group, tc.resource, got, tc.want)
			}
		})
	}
}
