package gitops

import (
	"fmt"
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
		{name: "colon in an RBAC name", id: "_system__metrics-server_rbac.authorization.k8s.io_ClusterRole", group: "rbac.authorization.k8s.io", kind: "ClusterRole", resource: "system:metrics-server", ok: true},
		{name: "colons in a namespaced RBAC name", id: "kube-system_cert-manager__leaderelection_rbac.authorization.k8s.io_Role", group: "rbac.authorization.k8s.io", kind: "Role", namespace: "kube-system", resource: "cert-manager:leaderelection", ok: true},
		{name: "invalid", id: "only_three_parts", ok: false},
		{name: "no underscore", id: "podinfo", ok: false},
		{name: "an empty name", id: "ns__apps_Deployment", ok: false},
		{name: "an empty kind", id: "ns_podinfo_apps_", ok: false},
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

// A Kustomization can list thousands of objects, and a search reads a
// manager's whole inventory to tell whether Radar can trust it.
func TestParseFluxInventoryIDAllocatesNothing(t *testing.T) {
	if n := testing.AllocsPerRun(100, func() { ParseFluxInventoryID("flux-system_my_weird_name_apps_Deployment") }); n != 0 {
		t.Fatalf("%v allocations per entry", n)
	}
}

// Telling whether Radar can read an inventory decodes no names, so an RBAC
// name Flux writes with a colon costs nothing either.
func TestFluxMembersReadableAllocatesNothing(t *testing.T) {
	entries := make([]any, 1000)
	for i := range entries {
		entries[i] = map[string]any{"id": fmt.Sprintf("kube-system_metrics-server__system__auth-delegator-%d_rbac.authorization.k8s.io_RoleBinding", i)}
	}
	ks := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"inventory": map[string]any{"entries": entries}}}}
	if !FluxMembersReadable(ks) {
		t.Fatal("unreadable")
	}
	if n := testing.AllocsPerRun(20, func() { FluxMembersReadable(ks) }); n != 0 {
		t.Fatalf("%v allocations per check of a 1,000-entry inventory", n)
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
		// Argo raises the guard only for auto-sync with prune and without allowEmpty.
		spec := map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}}}
		return &unstructured.Unstructured{Object: map[string]any{"spec": spec, "status": status}}
	}
	withPolicy := func(u *unstructured.Unstructured, automated map[string]any) *unstructured.Unstructured {
		if automated == nil {
			unstructured.RemoveNestedField(u.Object, "spec", "syncPolicy", "automated")
		} else {
			_ = unstructured.SetNestedMap(u.Object, automated, "spec", "syncPolicy", "automated")
		}
		return u
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
		// A deny window keeps the old condition after the policy changes; the
		// guard no longer holds once auto-sync can't prune everything.
		{"allowEmpty set", withPolicy(app("OutOfSync", res("Service", true)), map[string]any{"prune": true, "allowEmpty": true}), true, false},
		{"prune off", withPolicy(app("OutOfSync", res("Service", true)), map[string]any{"selfHeal": true}), true, false},
		{"auto-sync disabled in place", withPolicy(app("OutOfSync", res("Service", true)), map[string]any{"prune": true, "enabled": false}), true, false},
		{"manual", withPolicy(app("OutOfSync", res("Service", true)), nil), true, false},
	} {
		if got := ArgoSyncPrunesEverything(tc.app); got != tc.prunes {
			t.Errorf("%s: ArgoSyncPrunesEverything = %v, want %v", tc.name, got, tc.prunes)
		}
		if got := ArgoEmptyRenderGuardHolds(tc.app); got != tc.guardHolds {
			t.Errorf("%s: ArgoEmptyRenderGuardHolds = %v, want %v", tc.name, got, tc.guardHolds)
		}
	}
}

func TestToolForDecidesByAPIGroup(t *testing.T) {
	cases := []struct {
		group, kind, want string
	}{
		{"argoproj.io", "Application", ToolArgoCD},
		{"argoproj.io", "ApplicationSet", ToolArgoCD},
		{"argoproj.io", "AppProject", ToolArgoCD},
		{"kustomize.toolkit.fluxcd.io", "Kustomization", ToolFluxCD},
		{"helm.toolkit.fluxcd.io", "HelmRelease", ToolFluxCD},
		{"source.toolkit.fluxcd.io", "GitRepository", ToolFluxCD},
		// SIG Apps defines its own Application kind.
		{"app.k8s.io", "Application", ""},
		{"some.vendor", "FooApplication", ""},
		// argoproj.io is shared with Argo Rollouts and Workflows.
		{"argoproj.io", "Rollout", ""},
		{"argoproj.io", "Workflow", ""},
		{"", "Application", ""},
		{"fluxcd.io.example.com", "Kustomization", ""},
		{"fake.kustomize.toolkit.fluxcd.io", "Kustomization", ""},
		{"notargoproj.io", "Application", ""},
		{"source.toolkit.fluxcd.io", "Application", ""},
		{"source.toolkit.fluxcd.io", "Kustomization", ""},
		{"kustomize.toolkit.fluxcd.io", "HelmRelease", ""},
		{"source.toolkit.fluxcd.io", "HelmChart", ToolFluxCD},
		{"source.toolkit.fluxcd.io", "Bucket", ToolFluxCD},
		{"notification.toolkit.fluxcd.io", "Alert", ToolFluxCD},
		{"image.toolkit.fluxcd.io", "ImageRepository", ""},
	}
	for _, tc := range cases {
		if got := ToolFor(tc.group, tc.kind); got != tc.want {
			t.Errorf("ToolFor(%q, %q) = %q, want %q", tc.group, tc.kind, got, tc.want)
		}
	}
}
