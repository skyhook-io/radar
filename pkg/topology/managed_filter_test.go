package topology

import "testing"

func TestManagedResourceSetIncludesGitOpsDescendantsOnly(t *testing.T) {
	topo := &Topology{Nodes: []Node{
		{ID: "app", Kind: KindApplication, Name: "app", Data: map[string]any{"namespace": "argocd"}},
		{ID: "deployment/default/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "default"}},
		{ID: "deployment/default/unmanaged", Kind: KindDeployment, Name: "unmanaged", Data: map[string]any{"namespace": "default"}},
	}, Edges: []Edge{{Source: "app", Target: "deployment/default/web", Type: EdgeManages}}}
	set := ManagedResourceSet(topo)
	if !set[managedKey("Deployment", "default", "web")] || set[managedKey("Deployment", "default", "unmanaged")] {
		t.Fatalf("managed set = %#v", set)
	}
}

func TestIsManagedResourceUsesGitOpsMarkers(t *testing.T) {
	if !IsManagedResource(nil, "Deployment", "default", "web", map[string]string{"argocd.argoproj.io/instance": "app"}, nil) {
		t.Fatal("expected Argo instance marker")
	}
	if IsManagedResource(nil, "Deployment", "default", "web", map[string]string{"app.kubernetes.io/managed-by": "Helm"}, map[string]string{"meta.helm.sh/release-name": "web"}) {
		t.Fatal("native Helm metadata must not count as GitOps-managed")
	}
}
