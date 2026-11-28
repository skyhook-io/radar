package tree

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/topology"
)

func fluxObject(apiVersion, kind, namespace, name string, suspend bool, ready, reason string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": namespace, "generation": int64(2)},
		"spec":     map[string]any{"suspend": suspend},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": ready, "reason": reason, "observedGeneration": int64(2)},
		}},
	}}
	return obj
}

func healthyTopoNode(kind topology.NodeKind, apiVersion, namespace, name string) topology.Node {
	return topology.Node{
		ID: string(kind) + "/" + namespace + "/" + name, Kind: kind, Name: name, Status: topology.StatusHealthy,
		Data: map[string]any{"namespace": namespace, "apiVersion": apiVersion},
	}
}

// A Flux object a Kustomization applies is graded by its own conditions and
// spec.suspend, as on its own page, not by Radar's topology estimate.
func TestNestedFluxReconcilerReadsItsOwnStatus(t *testing.T) {
	parent := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system"},
		"spec":     map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}},
		"status": map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "podinfo_podinfo_helm.toolkit.fluxcd.io_HelmRelease", "v": "v2"},
			map[string]any{"id": "flux-system_infra_kustomize.toolkit.fluxcd.io_Kustomization", "v": "v1"},
			map[string]any{"id": "podinfo_gone_helm.toolkit.fluxcd.io_HelmRelease", "v": "v2"},
			map[string]any{"id": "podinfo_web_apps_Deployment", "v": "v1"},
		}}},
	}}
	parentRef := ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}
	hrRef := ResourceRef{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "podinfo", Name: "podinfo"}
	ksRef := ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "infra"}
	goneRef := ResourceRef{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "podinfo", Name: "gone"}
	depRef := ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "podinfo", Name: "web"}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(parentRef): parent,
		refKey(hrRef):     fluxObject("helm.toolkit.fluxcd.io/v2", "HelmRelease", "podinfo", "podinfo", true, "True", "UpgradeSucceeded"),
		refKey(ksRef):     fluxObject("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "infra", false, "False", "BuildFailed"),
		refKey(depRef):    {Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "podinfo"}}},
	}}
	topo := &topology.Topology{Nodes: []topology.Node{
		healthyTopoNode(topology.KindHelmRelease, "helm.toolkit.fluxcd.io/v2", "podinfo", "podinfo"),
		healthyTopoNode(topology.KindKustomization, "kustomize.toolkit.fluxcd.io/v1", "flux-system", "infra"),
		healthyTopoNode(topology.KindHelmRelease, "helm.toolkit.fluxcd.io/v2", "podinfo", "gone"),
		healthyTopoNode(topology.KindDeployment, "apps/v1", "podinfo", "web"),
	}}
	tree, _, err := newTestBuilder(t, getter, topo).Build(context.Background(), "Kustomization", "flux-system", "apps", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, tc := range []struct {
		name         string
		ref          ResourceRef
		sync, health string
		source       HealthSource
		tone         string
	}{
		{"suspended child HelmRelease", hrRef, "Synced", "Suspended", HealthSourceController, "degraded"},
		{"failing child Kustomization", ksRef, "OutOfSync", "Degraded", HealthSourceController, "unhealthy"},
		{"child whose object Radar could not read keeps the topology estimate", goneRef, "", "Healthy", HealthSourceRadar, "healthy"},
		{"non-Flux member keeps the topology estimate", depRef, "", "Healthy", HealthSourceRadar, "healthy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := findNode(t, tree, tc.ref)
			if n.Sync != tc.sync || n.Health != tc.health || n.HealthSource != tc.source || n.TopologyStatus != tc.tone {
				t.Errorf("node = sync %q health %q source %q tone %q, want %q/%q/%q/%q", n.Sync, n.Health, n.HealthSource, n.TopologyStatus, tc.sync, tc.health, tc.source, tc.tone)
			}
		})
	}
	if tree.Summary.Degraded != 1 || tree.Summary.OutOfSync != 1 {
		t.Errorf("Summary = %+v, want the failing child as the one degraded and out-of-sync resource", tree.Summary)
	}
}

// An Argo Application that deploys a Flux HelmRelease keeps Argo's verdict
// for it: only a Flux tree reads nested Flux objects' own status.
func TestArgoManagedFluxObjectKeepsArgoVerdict(t *testing.T) {
	app := healthProvenanceApp(map[string]any{
		"health": map[string]any{"status": "Healthy"},
		"resources": []any{
			map[string]any{"group": "helm.toolkit.fluxcd.io", "kind": "HelmRelease", "namespace": "podinfo", "name": "podinfo", "status": "Synced", "health": map[string]any{"status": "Healthy"}},
		},
	})
	hrRef := ResourceRef{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "podinfo", Name: "podinfo"}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}): app,
		refKey(hrRef): fluxObject("helm.toolkit.fluxcd.io/v2", "HelmRelease", "podinfo", "podinfo", true, "True", "UpgradeSucceeded"),
	}}
	topo := &topology.Topology{Nodes: []topology.Node{healthyTopoNode(topology.KindHelmRelease, "helm.toolkit.fluxcd.io/v2", "podinfo", "podinfo")}}
	tree, _, err := newTestBuilder(t, getter, topo).Build(context.Background(), "applications", "argocd", "billing", "argoproj.io")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	n := findNode(t, tree, hrRef)
	if n.Sync != "Synced" || n.Health != "Healthy" || n.HealthSource != HealthSourceController {
		t.Errorf("node = sync %q health %q source %q, want Argo's Synced/Healthy/controller", n.Sync, n.Health, n.HealthSource)
	}
}
