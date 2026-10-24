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
