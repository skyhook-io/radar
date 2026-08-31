package gitops

import "testing"

func TestParseFluxInventoryID(t *testing.T) {
	tests := []struct {
		name      string
		id        string
		group     string
		kind      string
		namespace string
		resource  string
		ok        bool
	}{
		{name: "deployment", id: "flux-system_podinfo_apps_Deployment", group: "apps", kind: "Deployment", namespace: "flux-system", resource: "podinfo", ok: true},
		{name: "underscores", id: "ns_my_weird_name_apps_Deployment", group: "apps", kind: "Deployment", namespace: "ns", resource: "my_weird_name", ok: true},
		{name: "core", id: "default_my-cm_core_ConfigMap", kind: "ConfigMap", namespace: "default", resource: "my-cm", ok: true},
		{name: "custom", id: "ml_train_batch.volcano.sh_Job", group: "batch.volcano.sh", kind: "Job", namespace: "ml", resource: "train", ok: true},
		{name: "cluster scoped", id: "_global_rbac.authorization.k8s.io_ClusterRole", group: "rbac.authorization.k8s.io", kind: "ClusterRole", resource: "global", ok: true},
		{name: "invalid", id: "only_three_parts", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			group, kind, namespace, resource, ok := ParseFluxInventoryID(test.id)
			if ok != test.ok || group != test.group || kind != test.kind || namespace != test.namespace || resource != test.resource {
				t.Fatalf("ParseFluxInventoryID(%q) = (%q, %q, %q, %q, %v)", test.id, group, kind, namespace, resource, ok)
			}
		})
	}
}
