package topology

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAddGitOpsManagedResourceEdgesPreservesArgoAPIGroup(t *testing.T) {
	nodes := []Node{
		{ID: "application/argocd/training", Kind: KindApplication, Name: "training", Data: map[string]any{"namespace": "argocd", "apiVersion": "argoproj.io/v1alpha1"}},
		{ID: "job/ml/train", Kind: KindJob, Name: "train", Data: map[string]any{"namespace": "ml"}},
		{ID: "job/ml/train/batch.volcano.sh", Kind: KindJob, Name: "train", Data: map[string]any{"namespace": "ml", "apiVersion": "batch.volcano.sh/v1alpha1"}},
	}
	app := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "argocd", "name": "training"},
		"status": map[string]any{"resources": []any{
			map[string]any{"group": "batch.volcano.sh", "kind": "Job", "namespace": "ml", "name": "train"},
		}},
	}}

	edges := addGitOpsManagedResourceEdges(
		nodes,
		nil,
		[]*unstructured.Unstructured{app},
		map[string]string{"argocd/training": "application/argocd/training"},
		nil,
		nil,
		nil,
	)
	if len(edges) != 1 || edges[0].Target != "job/ml/train/batch.volcano.sh" {
		t.Fatalf("Argo edges = %+v, want only the exact Volcano Job", edges)
	}
}

// Argo CD records each managed resource's actual group and omits it only for
// the core group, so the recorded group is exact: an entry without one names a
// core resource and never matches a batch Job of the same name.
func TestAddGitOpsManagedResourceEdgesTreatsArgoGroupAsExact(t *testing.T) {
	nodes := []Node{
		{ID: "application/argocd/training", Kind: KindApplication, Name: "training", Data: map[string]any{"namespace": "argocd", "apiVersion": "argoproj.io/v1alpha1"}},
		{ID: "job/ml/train", Kind: KindJob, Name: "train", Data: map[string]any{"namespace": "ml"}},
		{ID: "job/ml/train/batch.volcano.sh", Kind: KindJob, Name: "train", Data: map[string]any{"namespace": "ml", "apiVersion": "batch.volcano.sh/v1alpha1"}},
	}
	edgesFor := func(resource map[string]any) []Edge {
		app := &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": "argocd", "name": "training"},
			"spec":     map[string]any{"destination": map[string]any{"namespace": "ml"}},
			"status":   map[string]any{"resources": []any{resource}},
		}}
		return addGitOpsManagedResourceEdges(
			nodes,
			nil,
			[]*unstructured.Unstructured{app},
			map[string]string{"argocd/training": "application/argocd/training"},
			map[string]string{"application/argocd/training": "ml"},
			nil,
			nil,
		)
	}

	if edges := edgesFor(map[string]any{"group": "batch", "kind": "Job", "name": "train"}); len(edges) != 1 || edges[0].Target != "job/ml/train" {
		t.Fatalf("Argo edges = %+v, want only the built-in Job", edges)
	}
	if edges := edgesFor(map[string]any{"kind": "Job", "name": "train"}); len(edges) != 0 {
		t.Fatalf("a core-group Job entry matched %+v", edges)
	}
}

func TestAddGitOpsManagedResourceEdgesParsesFluxIdentityFromRight(t *testing.T) {
	nodes := []Node{
		{ID: "kustomization/flux-system/training", Kind: KindKustomization, Name: "training", Data: map[string]any{"namespace": "flux-system", "apiVersion": "kustomize.toolkit.fluxcd.io/v1"}},
		{ID: "job/ml/train_run", Kind: KindJob, Name: "train_run", Data: map[string]any{"namespace": "ml"}},
		{ID: "job/ml/train_run/batch.volcano.sh", Kind: KindJob, Name: "train_run", Data: map[string]any{"namespace": "ml", "apiVersion": "batch.volcano.sh/v1alpha1"}},
	}
	ks := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "flux-system", "name": "training"},
		"status": map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "ml_train_run_batch.volcano.sh_Job"},
		}}},
	}}

	edges := addGitOpsManagedResourceEdges(
		nodes,
		nil,
		nil,
		nil,
		nil,
		[]*unstructured.Unstructured{ks},
		map[string]string{"flux-system/training": "kustomization/flux-system/training"},
	)
	if len(edges) != 1 || edges[0].Target != "job/ml/train_run/batch.volcano.sh" {
		t.Fatalf("Flux edges = %+v, want only the exact underscore-named Volcano Job", edges)
	}
}

// On an Argo CD or Flux hub, objects that another cluster's Application or
// remote Kustomization lists are that cluster's; only a GitOps object that
// deploys here gets a manages edge to the same-named local object.
func TestAddGitOpsManagedResourceEdgesIgnoresRemoteDestinations(t *testing.T) {
	nodes := []Node{
		{ID: "application/argocd/sealed-hub", Kind: KindApplication, Name: "sealed-hub", Data: map[string]any{"namespace": "argocd", "apiVersion": "argoproj.io/v1alpha1"}},
		{ID: "application/argocd/sealed-prod", Kind: KindApplication, Name: "sealed-prod", Data: map[string]any{"namespace": "argocd", "apiVersion": "argoproj.io/v1alpha1"}},
		{ID: "kustomization/flux-system/fleet-prod", Kind: KindKustomization, Name: "fleet-prod", Data: map[string]any{"namespace": "flux-system", "apiVersion": "kustomize.toolkit.fluxcd.io/v1"}},
		{ID: "deployment/sealed-secrets/controller", Kind: KindDeployment, Name: "controller", Data: map[string]any{"namespace": "sealed-secrets"}},
	}
	app := func(name, server string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": "argocd", "name": name},
			"spec":     map[string]any{"destination": map[string]any{"server": server, "namespace": "sealed-secrets"}},
			"status": map[string]any{"resources": []any{
				map[string]any{"group": "apps", "kind": "Deployment", "namespace": "sealed-secrets", "name": "controller"},
			}},
		}}
	}
	remoteKs := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "flux-system", "name": "fleet-prod"},
		"spec":     map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod"}}},
		"status": map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "sealed-secrets_controller_apps_Deployment"},
		}}},
	}}

	edges := addGitOpsManagedResourceEdges(
		nodes,
		nil,
		[]*unstructured.Unstructured{app("sealed-prod", "https://prod.example.com"), app("sealed-hub", "https://kubernetes.default.svc")},
		map[string]string{"argocd/sealed-hub": "application/argocd/sealed-hub", "argocd/sealed-prod": "application/argocd/sealed-prod"},
		nil,
		[]*unstructured.Unstructured{remoteKs},
		map[string]string{"flux-system/fleet-prod": "kustomization/flux-system/fleet-prod"},
	)
	if len(edges) != 1 || edges[0].Source != "application/argocd/sealed-hub" {
		t.Fatalf("manages edges = %+v, want only the in-cluster Application's", edges)
	}
}
