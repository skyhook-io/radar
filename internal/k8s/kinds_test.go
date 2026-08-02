package k8s

import "testing"

func TestIsClusterOnlyKind(t *testing.T) {
	clusterOnly := []string{
		"nodes", "node", "Node",
		"persistentvolumes", "persistentvolume", "pv", "PV",
		"storageclasses", "storageclass", "sc",
		"volumeattachments", "volumeattachment",
		"ingressclasses", "ingressclass",
		"clusterroles", "clusterrole",
		"clusterrolebindings", "clusterrolebinding",
		"priorityclasses", "priorityclass",
		"runtimeclasses", "runtimeclass",
		"mutatingwebhookconfigurations", "mutatingwebhookconfiguration",
		"validatingwebhookconfigurations", "validatingwebhookconfiguration",
		"customresourcedefinitions", "customresourcedefinition", "crd",
	}
	for _, k := range clusterOnly {
		if !IsClusterOnlyKind(k) {
			t.Errorf("%q should be cluster-only", k)
		}
	}

	notClusterOnly := []string{
		// Namespaces is cluster-scoped at the K8s level but exposed as a
		// filtered list to restricted users — must NOT be blocked here.
		"namespaces", "namespace", "Namespace", "ns",
		// Namespaced kinds.
		"pods", "deployments", "secrets", "configmaps", "services",
		// Unknown.
		"made-up-kind", "",
	}
	for _, k := range notClusterOnly {
		if IsClusterOnlyKind(k) {
			t.Errorf("%q should NOT be flagged cluster-only", k)
		}
	}
}

func TestClusterOnlyKindGVR(t *testing.T) {
	cases := []struct {
		kind      string
		wantGroup string
		wantRes   string
		wantOK    bool
	}{
		{"nodes", "", "nodes", true},
		{"node", "", "nodes", true},
		{"pv", "", "persistentvolumes", true},
		{"namespaces", "", "namespaces", true}, // GVR exists even though IsClusterOnlyKind=false
		{"ns", "", "namespaces", true},
		{"clusterroles", "rbac.authorization.k8s.io", "clusterroles", true},
		{"volumeattachments", "storage.k8s.io", "volumeattachments", true},
		{"crd", "apiextensions.k8s.io", "customresourcedefinitions", true},
		{"NODES", "", "nodes", true}, // case-insensitive
		{"pods", "", "", false},
		{"unknown-kind", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			g, r, ok := ClusterOnlyKindGVR(tc.kind)
			if ok != tc.wantOK || g != tc.wantGroup || r != tc.wantRes {
				t.Errorf("ClusterOnlyKindGVR(%q) = (%q, %q, %v); want (%q, %q, %v)",
					tc.kind, g, r, ok, tc.wantGroup, tc.wantRes, tc.wantOK)
			}
		})
	}
}

func TestClassifyKindScope_StaticCatalogue(t *testing.T) {
	// Static catalogue must win even with no discovery wired up.
	clusterScoped, group, resource := ClassifyKindScope("nodes", "")
	if !clusterScoped || group != "" || resource != "nodes" {
		t.Errorf("nodes: got (%v, %q, %q); want (true, \"\", \"nodes\")", clusterScoped, group, resource)
	}

	clusterScoped, group, resource = ClassifyKindScope("clusterroles", "")
	if !clusterScoped || group != "rbac.authorization.k8s.io" || resource != "clusterroles" {
		t.Errorf("clusterroles: got (%v, %q, %q); want (true, \"rbac…\", \"clusterroles\")", clusterScoped, group, resource)
	}

	clusterScoped, _, _ = ClassifyKindScope("pods", "")
	if clusterScoped {
		t.Error("pods should not be cluster-scoped")
	}

	// A group hint MATCHING the builtin's canonical group still resolves the
	// builtin (clusterroles live in rbac.authorization.k8s.io).
	clusterScoped, group, resource = ClassifyKindScope("clusterroles", "rbac.authorization.k8s.io")
	if !clusterScoped || group != "rbac.authorization.k8s.io" || resource != "clusterroles" {
		t.Errorf("clusterroles with matching group: got (%v, %q, %q); want (true, \"rbac…\", \"clusterroles\")", clusterScoped, group, resource)
	}

	// A group hint that DISAGREES with the builtin's canonical group is a CRD
	// Kind collision (e.g. a CRD Kind=Node in another group), not the builtin —
	// the static catalogue must NOT win, or the CRD's reads would be authorized
	// against the builtin the caller can list. With no discovery, fail closed.
	clusterScoped, _, _ = ClassifyKindScope("nodes", "ignored.example.com")
	if clusterScoped {
		t.Error("nodes with a foreign group must not resolve to the builtin (collision guard)")
	}
}

func TestClassifyKindScope_NoDiscovery(t *testing.T) {
	// Without discovery, unknown kinds must NOT be classified cluster-scoped
	// (the gate falls through to namespace-based authorization).
	resourceDiscovery = nil
	t.Cleanup(func() { resourceDiscovery = nil })

	clusterScoped, _, _ := ClassifyKindScope("madeupcrd", "")
	if clusterScoped {
		t.Error("unknown kind without discovery should not be cluster-scoped")
	}
	clusterScoped, _, _ = ClassifyKindScope("madeupcrd", "made.up.io")
	if clusterScoped {
		t.Error("unknown kind with group but no discovery should not be cluster-scoped")
	}
}
