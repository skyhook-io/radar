package tree

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fakeDynamic struct {
	objects map[string]*unstructured.Unstructured
	calls   []ResourceRef
}

func (f *fakeDynamic) GetDynamicWithGroup(_ context.Context, kind string, namespace string, name string, group string) (*unstructured.Unstructured, error) {
	ref := ResourceRef{Group: group, Kind: kind, Namespace: namespace, Name: name}
	f.calls = append(f.calls, ref)
	if obj := f.objects[refKey(ref)]; obj != nil {
		return obj, nil
	}
	return f.objects[refKey(ResourceRef{Group: group, Kind: "Application", Namespace: namespace, Name: name})], nil
}

func TestBuildArgoTreeUsesManagedInventoryAndOwnershipEdges(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      "billing",
			"namespace": "argocd",
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Synced"},
			"health": map[string]any{"status": "Healthy"},
			"resources": []any{
				map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "billing", "status": "Synced", "health": map[string]any{"status": "Healthy"}},
				map[string]any{"kind": "Service", "namespace": "prod", "name": "billing", "status": "Synced", "health": map[string]any{"status": "Healthy"}},
			},
		},
	}}

	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "deployment/prod/billing", Kind: topology.KindDeployment, Name: "billing", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "prod", "group": "apps"}},
			{ID: "replicaset/prod/billing-abc", Kind: topology.KindReplicaSet, Name: "billing-abc", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "prod", "group": "apps"}},
			{ID: "pod/prod/billing-abc-1", Kind: topology.KindPod, Name: "billing-abc-1", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "prod"}},
			{ID: "service/prod/billing", Kind: topology.KindService, Name: "billing", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "prod"}},
		},
		Edges: []topology.Edge{
			{ID: "deployment-rs", Source: "deployment/prod/billing", Target: "replicaset/prod/billing-abc", Type: topology.EdgeManages},
			{ID: "rs-pod", Source: "replicaset/prod/billing-abc", Target: "pod/prod/billing-abc-1", Type: topology.EdgeManages},
			{ID: "service-pod", Source: "service/prod/billing", Target: "pod/prod/billing-abc-1", Type: topology.EdgeRoutesTo},
		},
	}

	dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}): app,
	}}
	builder := NewBuilder(dynamic, topo)

	tree, _, err := builder.Build(context.Background(), "applications", "argocd", "billing", "argoproj.io")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	assertNodeRole(t, tree, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}, RoleDeclared)
	assertNodeRole(t, tree, ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "prod", Name: "billing-abc"}, RoleGenerated)
	assertNodeRole(t, tree, ResourceRef{Kind: "Pod", Namespace: "prod", Name: "billing-abc-1"}, RoleGenerated)
	assertEdge(t, tree, nodeID(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}), nodeID(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}))
	assertEdge(t, tree, nodeID(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}), nodeID(ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "prod", Name: "billing-abc"}))
	assertNoEdge(t, tree, "service/prod/billing", "pod/prod/billing-abc-1")
}

func TestBuildDoesNotEnrichManagedResourcesOutsideAllowedNamespaces(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      "billing",
			"namespace": "argocd",
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Synced"},
			"health": map[string]any{"status": "Healthy"},
			"resources": []any{
				map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "billing", "status": "Synced"},
			},
		},
	}}
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      "billing",
			"namespace": "prod",
			"labels":    map[string]any{"secret-label": "should-not-leak"},
		},
	}}
	dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}): app,
		refKey(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}):           deployment,
	}}

	tree, _, err := NewBuilder(dynamic, nil).
		WithAllowedNamespaces([]string{"argocd"}).
		Build(context.Background(), "applications", "argocd", "billing", "argoproj.io")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	deploymentID := nodeID(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
	for _, node := range tree.Nodes {
		if node.ID != deploymentID {
			continue
		}
		if _, ok := node.Data["labels"]; ok {
			t.Fatalf("deployment node leaked labels from disallowed namespace: %#v", node.Data["labels"])
		}
		assertNoDynamicCall(t, dynamic, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
		return
	}
	t.Fatalf("deployment node %s not found", deploymentID)
}

func assertNodeRole(t *testing.T, tree *ResourceTree, ref ResourceRef, role NodeRole) {
	t.Helper()
	id := nodeID(ref)
	for _, node := range tree.Nodes {
		if node.ID == id {
			if node.Role != role {
				t.Fatalf("node %s role = %s, want %s", id, node.Role, role)
			}
			return
		}
	}
	t.Fatalf("node %s not found", id)
}

func assertEdge(t *testing.T, tree *ResourceTree, source string, target string) {
	t.Helper()
	for _, edge := range tree.Edges {
		if edge.Source == source && edge.Target == target {
			return
		}
	}
	t.Fatalf("edge %s -> %s not found", source, target)
}

func assertNoEdge(t *testing.T, tree *ResourceTree, source string, target string) {
	t.Helper()
	for _, edge := range tree.Edges {
		if edge.Source == source && edge.Target == target {
			t.Fatalf("unexpected edge %s -> %s", source, target)
		}
	}
}

func assertNoDynamicCall(t *testing.T, dynamic *fakeDynamic, ref ResourceRef) {
	t.Helper()
	for _, call := range dynamic.calls {
		if call.Group == ref.Group && call.Kind == ref.Kind && call.Namespace == ref.Namespace && call.Name == ref.Name {
			t.Fatalf("unexpected dynamic lookup for %#v", ref)
		}
	}
}
