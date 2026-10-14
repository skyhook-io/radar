package tree

import (
	"context"
	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
)

func TestBootstrapTreeStopsAtNestedManager(t *testing.T) {
	for _, tc := range []struct {
		kind, group string
		boundary    bool
	}{
		{"Kustomization", "kustomize.toolkit.fluxcd.io", true},
		{"HelmRelease", "helm.toolkit.fluxcd.io", true},
		{"Kustomization", "example.io", false},
	} {
		t.Run(tc.group, func(t *testing.T) {
			rootRef := ResourceRef{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Namespace: "flux-system", Name: "flux-system"}
			childRef := ResourceRef{Kind: tc.kind, Group: tc.group, Namespace: "flux-system", Name: "apps"}
			root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"namespace": "flux-system", "name": "flux-system"}, "status": map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "flux-system_apps_" + tc.group + "_" + tc.kind}, map[string]any{"id": "prod_direct_apps_Deployment"}}}}}}
			topo := &topology.Topology{Nodes: []topology.Node{
				{ID: "root", Kind: topology.KindKustomization, Name: rootRef.Name, Data: map[string]any{"namespace": rootRef.Namespace, "apiVersion": rootRef.Group + "/v1"}},
				{ID: "child", Kind: topology.NodeKind(tc.kind), Name: childRef.Name, Data: map[string]any{"namespace": childRef.Namespace, "apiVersion": childRef.Group + "/v1"}},
				{ID: "nested-deployment", Kind: topology.KindDeployment, Name: "nested", Data: map[string]any{"namespace": "prod", "group": "apps"}},
				{ID: "nested-pod", Kind: topology.KindPod, Name: "nested-pod", Data: map[string]any{"namespace": "prod"}},
				{ID: "direct-deployment", Kind: topology.KindDeployment, Name: "direct", Data: map[string]any{"namespace": "prod", "group": "apps"}},
				{ID: "direct-pod", Kind: topology.KindPod, Name: "direct-pod", Data: map[string]any{"namespace": "prod"}},
			}, Edges: []topology.Edge{
				{Source: "root", Target: "child", Type: topology.EdgeManages},
				{Source: "root", Target: "direct-deployment", Type: topology.EdgeManages},
				{Source: "child", Target: "nested-deployment", Type: topology.EdgeManages},
				{Source: "nested-deployment", Target: "nested-pod", Type: topology.EdgeManages},
				{Source: "direct-deployment", Target: "direct-pod", Type: topology.EdgeManages},
			}}
			dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(rootRef): root, refKey(ResourceRef{Kind: "kustomizations", Group: rootRef.Group, Namespace: rootRef.Namespace, Name: rootRef.Name}): root}}
			got, _, err := NewBuilder(dynamic, topo).Build(context.Background(), "kustomizations", rootRef.Namespace, rootRef.Name, rootRef.Group)
			if err != nil {
				t.Fatal(err)
			}
			assertNodeRole(t, got, childRef, RoleDeclared)
			assertNodeRole(t, got, ResourceRef{Kind: "Pod", Namespace: "prod", Name: "direct-pod"}, RoleGenerated)
			nested := false
			for _, node := range got.Nodes {
				if node.Ref.Name == "nested" || node.Ref.Name == "nested-pod" {
					nested = true
				}
			}
			if nested == tc.boundary {
				t.Fatalf("nested descendants=%v, manager boundary=%v", nested, tc.boundary)
			}
		})
	}
}
