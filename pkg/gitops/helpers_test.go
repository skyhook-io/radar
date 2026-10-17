package gitops

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

func TestFluxTargetsLocalCluster(t *testing.T) {
	local := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"path": "./apps"}}}
	remote := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod"}},
	}}}
	if !FluxTargetsLocalCluster(local) {
		t.Error("a Kustomization without kubeConfig applies locally")
	}
	if FluxTargetsLocalCluster(remote) {
		t.Error("a Kustomization with kubeConfig applies to another cluster")
	}
	if FluxTargetsLocalCluster(nil) {
		t.Error("nil must fail closed")
	}
}

// Shapes recorded from Argo CD 3.5.2: a live empty-render guard marks every
// resource requiresPruning; the same leftover condition after the render
// recovered and the Deployment drifted carries no requiresPruning at all.
func TestArgoEmptyRenderPredicates(t *testing.T) {
	app := func(sync string, resources ...any) *unstructured.Unstructured {
		status := map[string]any{"sync": map[string]any{"status": sync}}
		if resources != nil {
			status["resources"] = resources
		}
		return &unstructured.Unstructured{Object: map[string]any{"status": status}}
	}
	res := func(kind string, prune any) map[string]any {
		r := map[string]any{"kind": kind, "name": "guestbook-ui", "status": "OutOfSync"}
		if prune != nil {
			r["requiresPruning"] = prune
		}
		return r
	}
	for _, tc := range []struct {
		name               string
		app                *unstructured.Unstructured
		prunes, guardHolds bool
	}{
		{"real guard", app("OutOfSync", res("Service", true), res("Deployment", true)), true, true},
		{"stale guard plus drift", app("OutOfSync", res("Service", nil), res("Deployment", nil)), false, false},
		{"one resource still rendered", app("OutOfSync", res("Service", true), res("Deployment", false)), false, false},
		{"no resources", app("OutOfSync"), false, false},
		// IgnoreExtraneous keeps such an app Synced; auto-sync never runs, but
		// a manual sync with pruning still deletes everything.
		{"synced, everything extraneous", app("Synced", res("Service", true)), true, false},
		{"nil", nil, false, false},
	} {
		if got := ArgoSyncPrunesEverything(tc.app); got != tc.prunes {
			t.Errorf("%s: ArgoSyncPrunesEverything = %v, want %v", tc.name, got, tc.prunes)
		}
		if got := ArgoEmptyRenderGuardHolds(tc.app); got != tc.guardHolds {
			t.Errorf("%s: ArgoEmptyRenderGuardHolds = %v, want %v", tc.name, got, tc.guardHolds)
		}
	}
}
