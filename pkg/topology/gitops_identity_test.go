package topology

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestAddGitOpsManagedResourceEdgesPreservesArgoAPIGroup(t *testing.T) {
	nodes := []Node{
		{ID: "application/argocd/training", Kind: KindApplication, Name: "training", Data: map[string]any{"namespace": "argocd", "apiVersion": "argoproj.io/v1alpha1"}},
		{ID: "job/ml/train", Kind: KindJob, Name: "train", Data: map[string]any{"namespace": "ml"}},
		{ID: "job/ml/train/batch.volcano.sh", Kind: KindJob, Name: "train", Data: map[string]any{"namespace": "ml", "apiVersion": "batch.volcano.sh/v1alpha1"}},
	}
	app := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "argocd", "name": "training"},
		"spec":     map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
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
			"spec":     map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "ml"}},
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
		nil,
		nil,
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
		nil,
		nil,
	)
	if len(edges) != 1 || edges[0].Source != "application/argocd/sealed-hub" {
		t.Fatalf("manages edges = %+v, want only the in-cluster Application's", edges)
	}
}

// A HelmRelease manages what its status inventory records. Older
// helm-controllers record none; then Flux's origin labels name its members,
// wherever they live, except Helm hooks and objects a controller generated.
// The chart's instance label makes no claim, and a release applied to another
// cluster installed nothing here.
func TestBuildLinksHelmReleasesByInventoryElseOriginLabels(t *testing.T) {
	hrGVR := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	release := func(name string, spec, status map[string]any) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
			"metadata": map[string]any{"namespace": "flux-system", "name": name},
			"spec":     spec,
		}}
		if status != nil {
			obj.Object["status"] = status
		}
		return obj
	}
	inventory := func(ids ...string) map[string]any {
		entries := []any{}
		for _, id := range ids {
			entries = append(entries, map[string]any{"id": id, "v": "v1"})
		}
		return map[string]any{"inventory": map[string]any{"entries": entries}}
	}
	origin := func(name string) map[string]string {
		return map[string]string{"helm.toolkit.fluxcd.io/name": name, "helm.toolkit.fluxcd.io/namespace": "flux-system"}
	}
	deployment := func(name string, labels map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: name, Labels: labels}}
	}
	hook := deployment("hook", origin("labelled"))
	hook.Annotations = map[string]string{"helm.sh/hook": "post-install"}
	generated := deployment("generated", origin("labelled"))
	controller := true
	generated.OwnerReferences = []metav1.OwnerReference{{APIVersion: "example.io/v1", Kind: "Operand", Name: "op", UID: "op", Controller: &controller}}

	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{hrGVR},
		kinds:   map[schema.GroupVersionResource]string{hrGVR: "HelmRelease"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{hrGVR: {
			release("recorded", map[string]any{}, inventory("apps_listed_apps_Deployment")),
			release("empty", map[string]any{}, inventory()),
			release("labelled", map[string]any{}, nil),
			release("remote", map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod"}}}, nil),
		}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{deployments: []*appsv1.Deployment{
		deployment("listed", nil),
		deployment("stale", origin("empty")),
		deployment("web", origin("labelled")),
		deployment("instance", map[string]string{"app.kubernetes.io/instance": "labelled"}),
		deployment("api", origin("remote")),
		hook,
		generated,
	}}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := map[string][]string{}
	for _, e := range topo.Edges {
		if strings.HasPrefix(e.Source, "helmrelease/") {
			got[e.Target] = append(got[e.Target], e.Source)
		}
	}
	want := map[string][]string{
		"deployment/apps/listed": {"helmrelease/flux-system/recorded"},
		"deployment/apps/web":    {"helmrelease/flux-system/labelled"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HelmRelease edges = %v, want %v", got, want)
	}
}

// A HelmRelease's auto neighborhood reaches what its inventory records, in
// another namespace too, as well as its chart source.
func TestHelmReleaseNeighborhoodReachesRecordedWorkloads(t *testing.T) {
	hrGVR := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	release := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "flux-system", "name": "podinfo"},
		"spec":     map[string]any{"targetNamespace": "apps"},
		"status":   map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "apps_podinfo_apps_Deployment", "v": "v1"}}}},
	}}
	dynamic := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{hrGVR},
		kinds:     map[schema.GroupVersionResource]string{hrGVR: "HelmRelease"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{hrGVR: {release}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{deployments: []*appsv1.Deployment{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "podinfo"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "unrelated", Labels: map[string]string{"app.kubernetes.io/instance": "podinfo"}}},
	}}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	sub := BuildNeighborhood(topo, ResourceRef{Kind: "HelmRelease", Namespace: "flux-system", Name: "podinfo", Group: "helm.toolkit.fluxcd.io"}, NeighborhoodOptions{Profile: ProfileAuto, Hops: 1})
	got := map[string]bool{}
	for _, n := range sub.Nodes {
		got[n.ID] = true
	}
	if !got["deployment/apps/podinfo"] || got["deployment/apps/unrelated"] {
		t.Fatalf("HelmRelease neighborhood = %v, want its recorded Deployment and not the instance-label match", slices.Sorted(maps.Keys(got)))
	}
}
